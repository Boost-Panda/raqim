// raqim: a coding harness with persistent memory. gen-0 kernel.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aliirz/raqim/internal/agent"
	"github.com/aliirz/raqim/internal/config"
	"github.com/aliirz/raqim/internal/echo"
	"github.com/aliirz/raqim/internal/memory"
	"github.com/aliirz/raqim/internal/permission"
	"github.com/aliirz/raqim/internal/provider"
	"github.com/aliirz/raqim/internal/session"
	"github.com/aliirz/raqim/internal/tools"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "raqim:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	memPath := cfg.MemoryPath()
	if err := memory.InitRepo(memPath); err != nil {
		return err
	}
	mc := config.LoadMemConfig(memPath)
	prov := provider.NewAnthropic()
	ctx := context.Background()

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "echo":
		if len(args) < 2 {
			return errors.New("usage: raqim echo <session-id>")
		}
		return echo.Run(ctx, prov, cfg, args[1])
	case "reindex":
		return reindex(cfg, memPath, mc.Budgets.IndexTokens)
	case "dream", "memory":
		return fmt.Errorf("%q is reserved for a future gen", cmd)
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		p := fs.String("p", "", "one-shot prompt")
		fs.Parse(args[1:])
		if *p == "" {
			return errors.New("usage: raqim run -p \"...\"")
		}
		return interactive(ctx, prov, cfg, mc, *p)
	case "":
		return interactive(ctx, prov, cfg, mc, "")
	default:
		return fmt.Errorf("unknown subcommand %q (have: run, echo, reindex)", cmd)
	}
}

func interactive(ctx context.Context, prov provider.Provider, cfg *config.Config, mc config.MemConfig, oneShot string) error {
	cwd, _ := os.Getwd()
	memPath := cfg.MemoryPath()
	stdin := bufio.NewReader(os.Stdin)

	project, gitRoot, found := cfg.ResolveProject(cwd)
	if !found {
		fmt.Printf("unmapped repo %s\nmap this repo to a project (new or existing) [%s]: ", gitRoot, filepath.Base(gitRoot))
		line, _ := stdin.ReadString('\n')
		project = strings.TrimSpace(line)
		if project == "" {
			project = filepath.Base(gitRoot)
		}
		if err := cfg.MapRepo(project, gitRoot); err != nil {
			return err
		}
		fmt.Printf("mapped %s → project %q\n", gitRoot, project)
	}

	distilled := func(id string) bool { return memory.HasEchoCommit(memPath, id) }
	session.Reap(mc.Sessions.TTLDays, distilled, os.Stdout)
	flagUndistilledLogs(memPath)
	recover := session.Undistilled(distilled)
	if len(recover) > 0 && oneShot == "" {
		fmt.Printf("%d undistilled session(s) found. distill now? [y/N]: ", len(recover))
		line, _ := stdin.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) == "y" {
			for id := range recover {
				if err := echo.Run(ctx, prov, cfg, id); err != nil {
					fmt.Fprintf(os.Stderr, "[echo] %s: %v\n", id, err)
				}
			}
		}
	}

	sess, err := session.New(project, cfg.Model.Agent)
	if err != nil {
		return err
	}
	a := &agent.Agent{
		Prov: prov, Model: cfg.Model.Agent,
		Perm: permission.New(stdin, os.Stdout, cwd, config.RaqimDir()),
		Sess: sess, Out: os.Stdout, WarnPct: cfg.Context.WarnPct,
		ToolCtx: &tools.Ctx{Cwd: cwd, MemoryPath: memPath, Project: project, Touched: map[string]bool{}},
	}
	a.Init(memory.Inject(memPath, project, mc.Budgets.InjectTokens))

	// ctrl-c at any prompt state (readline, permission prompt, why-prompt,
	// mid-api-call): end event + echo pass, then exit. the handler runs in
	// its own goroutine and calls os.Exit, so a blocked stdin read can
	// never swallow it.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Printf("\n[interrupt — session %s ending, running the echo pass]\n", sess.ID)
		sess.End("interrupt")
		if err := echo.Run(ctx, prov, cfg, sess.ID); err != nil {
			fmt.Fprintf(os.Stderr, "[echo] %v\n", err)
		}
		os.Exit(130)
	}()

	endAndDistill := func(reason string) error {
		sess.End(reason)
		fmt.Printf("[session %s ended: %s — running the echo pass]\n", sess.ID, reason)
		return echo.Run(ctx, prov, cfg, sess.ID)
	}

	if oneShot != "" {
		err := a.Turn(ctx, oneShot)
		if err != nil && !errors.Is(err, agent.ErrContextLimit) && !errors.Is(err, agent.ErrUserExit) {
			sess.End("error")
			return err
		}
		return endAndDistill("exit")
	}

	fmt.Printf("raqim · project %s · model %s · session %s (/exit to end)\n", project, cfg.Model.Agent, sess.ID)
	for {
		fmt.Print("\nraqim> ")
		line, rerr := stdin.ReadString('\n')
		text := strings.TrimSpace(line)
		if rerr != nil || text == "/exit" {
			return endAndDistill("exit")
		}
		if text == "" {
			continue
		}
		if err := a.Turn(ctx, text); err != nil {
			if errors.Is(err, agent.ErrContextLimit) {
				fmt.Println("[context at 95% — hard stop]")
				return endAndDistill("context_limit")
			}
			if errors.Is(err, agent.ErrUserExit) {
				return endAndDistill("exit")
			}
			fmt.Fprintf(os.Stderr, "[error] %v\n", err)
		}
	}
}

func reindex(cfg *config.Config, memPath string, budget int) error {
	dirs, _ := filepath.Glob(filepath.Join(memPath, "projects", "*"))
	changed := 0
	for _, d := range dirs {
		project := filepath.Base(d)
		ch, err := memory.WriteIndex(memPath, project, budget)
		if err != nil {
			return err
		}
		if ch {
			changed++
			fmt.Printf("reindexed %s\n", project)
		}
	}
	if changed == 0 {
		fmt.Println("reindex: no changes (clean tree)")
		return nil
	}
	return memory.CommitAll(memPath, fmt.Sprintf("reindex: regenerated %d index(es)", changed))
}

func flagUndistilledLogs(memPath string) {
	matches, _ := filepath.Glob(filepath.Join(memPath, "projects", "*", "log", "*-UNDISTILLED.md"))
	for _, m := range matches {
		fmt.Printf("[warn] undistilled echo output needs review: %s\n", m)
	}
}
