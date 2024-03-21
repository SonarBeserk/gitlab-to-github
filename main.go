package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/google/go-github/v33/github"
	gitlab "github.com/xanzy/go-gitlab"
	"golang.org/x/oauth2"
)

var (
	gitlabOrg   = flag.String("gitlaborg", "", "The name of the gitlab org, left blank for user repos")
	gitlabToken = flag.String("gitlabtoken", "", "The token used to access the github api")

	githubOrg   = flag.String("githuborg", "", "The name of the github org, left blank for user repos")
	githubToken = flag.String("githubtoken", "", "The token used to access the github api")
)

func main() {
	flag.Parse()

	gitlabClient, err := gitlab.NewClient(*gitlabToken)
	if err != nil {
		log.Fatalf("Failed to create Gitlab client: %v", err)
	}

	ctx := context.Background()
	tokenSrc := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: *githubToken},
	)
	oauthClient := oauth2.NewClient(ctx, tokenSrc)
	githubClient := github.NewClient(oauthClient)

	projects, err := fetchGitlabProjects(gitlabClient)
	if err != nil {
		fmt.Printf("Error fetching Gitlab projects: %v\n", err)
		return
	}

	// Grab stdin so we can have a confirmation prompt
	reader := bufio.NewReader(os.Stdin)

	// Confirm the list of repositories to copy
	projectNames := []string{}
	for _, project := range projects {
		projectNames = append(projectNames, project.Name)
	}

	fmt.Println("Are you sure you wish to copy the following repostories?")
	fmt.Println(strings.Join(projectNames, "\n"))
	fmt.Println("[yes/No]")

	var answer string
	answer, err = reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			fmt.Println("Process cancelled, exiting.")
			return
		}

		fmt.Printf("Error reading confirmation prompt: %v\n", err)
		return
	}

	if !strings.EqualFold(answer, "yes") {
		fmt.Println("Process cancelled, exiting.")
		return
	}

	var allRepos []*github.Repository
	allRepos, err = fetchGithubRepositories(ctx, githubClient)
	if err != nil {
		fmt.Printf("Error fetching Github repositories: %v\n", err)
		return
	}

	ignoredRepos := copyRepositories(ctx, githubClient, projects, allRepos)
	if len(ignoredRepos) > 0 {
		fmt.Printf("Ignored: [%v]\n", strings.Join(ignoredRepos, ", "))
	}
}

func fetchGitlabProjects(client *gitlab.Client) ([]*gitlab.Project, error) {
	var projects []*gitlab.Project

	opt := &gitlab.ListGroupProjectsOptions{
		IncludeSubgroups: gitlab.Bool(true),
	}

	var org string
	if githubOrg != nil {
		org = *githubOrg
	}

	if org == "" {
		proj, _, err := client.Projects.ListUserProjects("", nil)
		if err != nil {
			return nil, fmt.Errorf("error listing Gitlab projects: %v", err)
		}

		projects = proj
	} else {
		proj, _, err := client.Groups.ListGroupProjects(org, opt)
		if err != nil {
			return nil, fmt.Errorf("error listing Gitlab projects: %v", err)
		}

		projects = proj
	}

	return projects, nil
}

func fetchGithubRepositories(ctx context.Context, client *github.Client) ([]*github.Repository, error) {
	userListopt := &github.RepositoryListOptions{
		ListOptions: github.ListOptions{PerPage: 10},
	}

	orgListopt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{PerPage: 10},
	}

	// get all pages of results
	var allRepos []*github.Repository
	for {
		var repos []*github.Repository
		var resp *github.Response
		var err error

		if *githubOrg == "" {
			repos, resp, err = client.Repositories.List(ctx, *githubOrg, userListopt)
		} else {
			repos, resp, err = client.Repositories.ListByOrg(ctx, *githubOrg, orgListopt)
		}

		if err != nil {
			return nil, fmt.Errorf("error listing Github projects list: %v", err)
		}
		allRepos = append(allRepos, repos...)
		if resp.NextPage == 0 {
			break
		}
		if *githubOrg == "" {
			userListopt.Page = resp.NextPage
		} else {
			orgListopt.Page = resp.NextPage
		}
	}

	return allRepos, nil
}

func copyRepositories(ctx context.Context, githubClient *github.Client, gitlabProjects []*gitlab.Project, githubProjects []*github.Repository) []string {
	ignoredRepos := []string{}
	for _, project := range gitlabProjects {
		found := false

		for _, repo := range githubProjects {
			projName := project.Name
			projName = strings.ReplaceAll(projName, " ", "-")

			if strings.EqualFold(projName, *repo.Name) {
				ignoredRepos = append(ignoredRepos, project.Name)
				found = true
				continue
			}
		}

		if found {
			continue
		}

		fmt.Printf("Creating Github repo for %v\n", project.Name)

		newRepo := &github.Repository{
			Name:          github.String(project.Name),
			Description:   github.String(project.Description),
			Private:       github.Bool(project.Visibility == gitlab.PrivateVisibility),
			DefaultBranch: github.String(project.DefaultBranch),
			Topics:        project.TagList,
		}

		repo, _, err := githubClient.Repositories.Create(ctx, *githubOrg, newRepo)
		if err != nil {
			fmt.Printf("Error creating Github repo: %v\n", err)
			continue
		}

		fmt.Printf("Cloning %v to push up\n", project.SSHURLToRepo)

		path, err := os.Getwd()
		if err != nil {
			log.Printf("Error finding working directory %v\n", err)
			continue
		}

		clonePath := path + string(os.PathSeparator) + "Repositories" + string(os.PathSeparator) + project.Name

		err = os.RemoveAll(clonePath)
		if err != nil {
			fmt.Printf("Error cleaning up folder %v\n", err)
			continue
		}

		cloneCmd := exec.Command("git", "clone", "--mirror", project.SSHURLToRepo, clonePath)

		err = cloneCmd.Run()
		if err != nil {
			fmt.Printf("Error cloning %v\n", err)
			continue
		}

		pushCmd := exec.Command("git", "push", "--mirror", *repo.SSHURL)
		pushCmd.Dir = clonePath
		fmt.Printf("Pushing up project %v\n", project.Name)

		err = pushCmd.Run()
		if err != nil {
			log.Printf("Error pushing mirrored repository %v\n", err)
			continue
		}
	}

	return ignoredRepos
}
