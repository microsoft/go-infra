package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/microsoft/go-infra/githubutil"
	"github.com/microsoft/go-infra/subcmd"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "github-who-am-i",
		Summary: "Print the identity of the specified GitHub user. Intended for debugging.",
		Handle:  handleWhoAmI,
	})
}

func handleWhoAmI(p subcmd.ParseFunc) error {
	auth := githubutil.BindGitHubAuthFlags("")

	if err := p(); err != nil {
		return err
	}

	log.SetPrefix("github-who-am-i: ")
	log.SetFlags(0)

	ctx := context.Background()
	if err := accordingToAuther(auth); err != nil {
		log.Printf("error in accordingToAuther: %v\n", err)
	}
	if err := accordingToUser(ctx, auth); err != nil {
		log.Printf("error in accordingToUser: %v\n", err)
	}
	if err := accordingToApp(ctx, auth); err != nil {
		log.Printf("error in accordingToApp: %v\n", err)
	}
	return nil
}

func accordingToAuther(f *githubutil.GitHubAuthFlags) error {
	identity, err := f.NewAuther()
	if err != nil {
		return err
	}
	id, err := identity.GetIdentity()
	if err != nil {
		return err
	}
	log.Printf("According to NewAuther: %s\n", id)
	return nil
}

func accordingToUser(ctx context.Context, f *githubutil.GitHubAuthFlags) error {
	client, err := f.NewClient(ctx)
	if err != nil {
		return err
	}
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(user, "  ", "  ")
	if err != nil {
		return err
	}
	log.Printf("According to client.Users.Get: %s\n", data)
	return nil
}

func accordingToApp(ctx context.Context, f *githubutil.GitHubAuthFlags) error {
	client, err := f.NewAppClient(ctx)
	if err != nil {
		return err
	}
	app, _, err := client.Apps.Get(ctx, "")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(app, "  ", "  ")
	if err != nil {
		return err
	}
	log.Printf("According to client.Apps.Get: %s\n", data)
	return nil
}
