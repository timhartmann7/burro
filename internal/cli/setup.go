package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/timhartmann7/burro/internal/config"
	"github.com/timhartmann7/burro/internal/initcmd"
	"github.com/timhartmann7/burro/internal/output"
	"github.com/timhartmann7/burro/internal/tree"
)

func (a *App) initProject(ctx context.Context, p *parsed) error {
	if p.flags["print"] == flagSet {
		return p.print.Rules(initcmd.Block())
	}
	dir, err := os.Getwd()
	if err != nil {
		return output.NewError(output.ExitUsage, fmt.Sprintf("working folder: %v", err),
			"run burro inside the project's git repository")
	}
	repo, err := tree.Open(ctx, dir, a.env)
	if err != nil {
		return err
	}
	agentsFile := p.flags["agents-file"]
	if agentsFile != "" {
		rel, ok := config.InRoot(repo.Root(), dir, agentsFile)
		if !ok || rel == "." {
			return output.NewError(output.ExitUsage, fmt.Sprintf("init: --agents-file %s is not a file inside the project folder %s", agentsFile, repo.Root()),
				"pass a file inside the project's git repository, for example --agents-file docs/AGENTS.md")
		}
		agentsFile = rel
	}
	host, err := a.onlyHost()
	if err != nil {
		return err
	}
	files, err := initcmd.New(repo.Root(), host).Run(agentsFile)
	if err != nil {
		return err
	}
	if err := p.print.Init(files); err != nil {
		return err
	}
	if p.human {
		a.note(`next: open your agent here and say "fill in burro.toml and run the tests with burro"`+"\n",
			"      or edit it yourself: burro help config\n")
	}
	return nil
}

// onlyHost is the one host burro knows, or empty when there are none or
// several to choose from.
func (a *App) onlyHost() (string, error) {
	store, err := a.store()
	if err != nil {
		return "", err
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		return "", err
	}
	return list[0].Name(), nil
}
