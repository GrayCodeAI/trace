package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
)

func projectCommand(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: trace project <list|create|card-add|card-move> ...")
	}
	fs := flag.NewFlagSet("project "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	name := fs.String("name", "", "project name")
	description := fs.String("description", "", "project description")
	kind := fs.String("kind", "", "card kind: issue or pull")
	number := fs.Int("number", 0, "issue or pull request number")
	title := fs.String("title", "", "card title")
	column := fs.String("column", "", "project column")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	store, err := openStore(*data)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) {
			return errors.New("usage: trace project list [-data DIR] OWNER/NAME")
		}
		projects, err := store.listProjects(fs.Arg(0))
		if err != nil {
			return err
		}
		for _, p := range projects {
			fmt.Printf("%d\t%s\n", p.ID, p.Name)
		}
		return nil
	case "create":
		if fs.NArg() != 1 || !validRepoName(fs.Arg(0)) || strings.TrimSpace(*name) == "" {
			return errors.New("usage: trace project create [-data DIR] -name NAME [-description TEXT] OWNER/NAME")
		}
		p, err := store.createProject(fs.Arg(0), *name, *description)
		if err != nil {
			return err
		}
		fmt.Println("created project", p.ID, p.Name)
		return nil
	case "card-add":
		if fs.NArg() != 2 || !validRepoName(fs.Arg(0)) || *number < 1 || *title == "" {
			return errors.New("usage: trace project card-add [-data DIR] -kind issue|pull -number N -title TITLE OWNER/NAME PROJECT_ID")
		}
		id, err := strconv.Atoi(fs.Arg(1))
		if err != nil {
			return err
		}
		card, err := store.addProjectCard(fs.Arg(0), id, *kind, *number, *title, *column)
		if err != nil {
			return err
		}
		fmt.Println("added card", card.ID)
		return nil
	case "card-move":
		if fs.NArg() != 3 || !validRepoName(fs.Arg(0)) || *column == "" {
			return errors.New("usage: trace project card-move [-data DIR] -column NAME OWNER/NAME PROJECT_ID CARD_ID")
		}
		projectID, err := strconv.Atoi(fs.Arg(1))
		if err != nil {
			return err
		}
		cardID, err := strconv.Atoi(fs.Arg(2))
		if err != nil {
			return err
		}
		card, err := store.moveProjectCard(fs.Arg(0), projectID, cardID, *column)
		if err != nil {
			return err
		}
		fmt.Println("moved card", card.ID, "to", card.Column)
		return nil
	default:
		return errors.New("usage: trace project <list|create|card-add|card-move> ...")
	}
}
