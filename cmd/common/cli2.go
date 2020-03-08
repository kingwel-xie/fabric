package common

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kingwel-xie/readline"
	"github.com/urfave/cli"

	"github.com/hyperledger/fabric/common/flogging"
)

var (
	ErrBadArgument = fmt.Errorf("bad argument")
	ErrQuitCLI     = fmt.Errorf("quitting cli")
)

// built-in cli commands
//
var builtinCmds = cli.Commands{
	&cli.Command{
		Category:    "builtin",
		Name:        "test",
		Usage:       "test",
		Description: "test command cancellation",
		Action: func(c *cli.Context) error {
			done := make(chan struct{}, 1)
			tick := time.NewTicker(time.Second)
			go func() {
				var count = 1
			loop:
				for {
					select {
					case <-tick.C:
						fmt.Println("Tick...", count)
						count++
						if count >= 10 {
							break loop
						}
					case <-c.Context.Done():
						fmt.Fprintln(c.App.Writer, c.Context.Err())
						break loop
					}
				}
				done <- struct{}{}
			}()

			<-done
			fmt.Fprintln(c.App.Writer, "Done")

			return nil
		},
	},
	&cli.Command{
		Category:    "builtin",
		Name:        "tree",
		Usage:       "tree",
		Description: "show commands in tree",
		Action: func(c *cli.Context) error {
			rl := c.App.Metadata["readline"].(*readline.Instance)
			tree := rl.Config.AutoComplete.(*readline.PrefixCompleter)
			fmt.Fprint(c.App.Writer, tree.Tree(""))
			return nil
		},
	}, &cli.Command{
		Category:    "builtin",
		Name:        "prompt",
		Usage:       "prompt",
		Aliases:     []string{"pro"},
		Description: "set cli prompt",
		Action: func(c *cli.Context) error {
			rl := c.App.Metadata["readline"].(*readline.Instance)
			if c.Args().Present() {
				rl.SetPrompt(c.Args().First())
			}
			return nil
		},
	},
	&cli.Command{
		Category:    "builtin",
		Name:        "mode",
		Usage:       "mode [vi|emacs]",
		Description: "set vi or emacs mode",
		Action: func(c *cli.Context) error {
			rl := c.App.Metadata["readline"].(*readline.Instance)
			switch c.Args().First() {
			case "vi":
				rl.SetVimMode(true)
			case "emacs":
				rl.SetVimMode(false)
			case "":
				if rl.IsVimMode() {
					fmt.Fprintln(c.App.Writer, "current mode: vim")
				} else {
					fmt.Fprintln(c.App.Writer, "current mode: emacs")
				}
			default:
				return ErrBadArgument
			}
			return nil
		},
	},
	&cli.Command{
		Category:    "builtin",
		Name:        "exit",
		Aliases:     []string{"quit"},
		Usage:       "exit",
		Description: "exit CLI",
		Action: func(c *cli.Context) error {
			return ErrQuitCLI
		},
	},
	&cli.Command{
		Category:    "builtin",
		Name:        "log",
		Usage:       "log",
		Description: "log management",
		Subcommands: []*cli.Command{
			&cli.Command{
				Name:    "ls",
				Aliases: []string{"l"},
				Usage:   "ls",
				Action: func(c *cli.Context) error {
					l := flogging.Loggers()
					sort.Strings(l)
					for _, n := range l {
						fmt.Fprintf(c.App.Writer, "%40s : %s \n", n, flogging.LoggerLevel(n))
					}
					return nil
				},
			},
			&cli.Command{
				Name:    "level",
				Aliases: []string{"lvl"},
				Usage:   "level",
				Action: func(c *cli.Context) error {
					if c.Args().Len() != 2 {
						fmt.Fprintln(c.App.Writer, "Bad argument")
						return ErrBadArgument
					}

					var s string
					if c.Args().First() == "all" {
						s = fmt.Sprintf("%s", c.Args().Get(1))
					} else {
						s = fmt.Sprintf("%s=%s", c.Args().First(), c.Args().Get(1))
					}

					defer func() {
						err := recover()
						if err != nil {
							fmt.Println(err)
						}
					}()

					flogging.ActivateSpec(s)
					return nil
				},
			},
		},
	},
}

func addChildren(parent *readline.PrefixCompleter, children ...readline.PrefixCompleterInterface) {
	parent.Children = append(parent.Children, children...)
}

func generateCompleter(root *readline.PrefixCompleter, cmds cli.Commands) *readline.PrefixCompleter {
	if root == nil {
		root = readline.PcItem("")
		// fix help
		addChildren(root, readline.PcItem("help"))
	}
	for _, cmd := range cmds {
		n := readline.PcItem(cmd.Name)
		if cmd.Subcommands != nil {
			generateCompleter(n, cmd.Subcommands)
		}
		addChildren(root, n)
	}

	return root
}

func filterInput(r rune) (rune, bool) {
	switch r {
	// block CtrlZ feature
	case readline.CharCtrlZ:
		return r, false
	}
	return r, true
}

func RunCli(prompt string, cmds cli.Commands, f func(map[string]interface{})) {
	fmt.Println("Spawning CLI...")

	// install builtin commands
	fullCmds := make(cli.Commands, 0, 100)
	fullCmds = append(fullCmds, builtinCmds...)
	fullCmds = append(fullCmds, cmds...)
	completer := generateCompleter(nil, fullCmds)

	//fmt.Println(completer.Tree(""))

	inDebugger := strings.Contains(os.Args[0], "go_build")

	if inDebugger {
		readline.Stdin = os.Stdin
		readline.Stdout = os.Stdout
		readline.Stderr = os.Stderr
		prompt += "# "
	} else {
		prompt += "\033[31m$\033[0m "
	}

	// make rl at first, but without AutoComplete specified
	rl, err := readline.NewEx(&readline.Config{
		Prompt:              prompt,
		AutoComplete:        completer,
		InterruptPrompt:     "interrupt",
		EOFPrompt:           "exit",
		HistorySearchFold:   true,
		FuncFilterInputRune: filterInput,
	})
	if err != nil {
		panic(err)
	}
	defer rl.Close()

	// make cli app
	app := setupCLI(fullCmds, rl, f)

	// setup context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	//log.SetOutput(rl.Stderr())
	for {
		ln := rl.Line()
		if ln.CanBreak() {
			if inDebugger {
				fmt.Println("Quit if in debugger, got", ln)
				break
			}
			rl.SetPrompt("Do you really want to quit? [y/n]")
			ln = rl.Line()
			//ln = rl.Line()
			if ln.Line == "Y" || ln.Line == "y" {
				break
			}
			rl.SetPrompt(rl.Config.Prompt)
			continue
		}
		//log.Println("readline:", ln.Line)

		line := strings.TrimSpace(ln.Line)
		var args []string = []string{"CLI"}
		if line != "" {
			cmdargs := strings.Split(line, " ")
			args = append(args, cmdargs...)
		}

		// create command context
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{}, 1)

		// routine to monitor the terminal for Break ^C ^D
		go func() {
			rl.Terminal.EnterRawMode()
			f := rl.Config.FuncFilterInputRune
			rl.Config.FuncFilterInputRune = func(r rune) (rune, bool) {
				switch r {
				case readline.CharInterrupt, readline.CharEnter, readline.CharCtrlJ, readline.CharDelete:
					return r, true
				}
				// block all other runes
				//fmt.Println("filtered", r)
				return r, false
			}
			rl.SetPrompt("")
			defer func() {
				rl.Terminal.ExitRawMode()
				rl.Config.FuncFilterInputRune = f
				rl.SetPrompt(rl.Config.Prompt)
				cancel()
				done <- struct{}{}
				//fmt.Println("DOne routine")
			}()

			for {
				select {
				case <-ctx.Done():
					return
				case <-rl.Operation.OutChan():
				case err := <-rl.Operation.ErrChan():
					if _, ok := err.(*readline.InterruptError); ok {
						//fmt.Println("GOt int")
						return
					} else if err == io.EOF {
						//fmt.Println("GOt eof")
						return
					} else {
						//fmt.Println("GOt", err)
					}
				}
			}
		}()

		e := app.RunContext(ctx, args)
		if e != nil {
			if e == ErrQuitCLI {
				break
			}
		}

		cancel()
		// wait for monitor routine finished
		<-done
		//rl.Operation
		//fmt.Println("DOne...")
	}
	os.Exit(1)
}

func setupCLI(cmds []*cli.Command, rl *readline.Instance, f func(map[string]interface{})) *cli.App {

	cli.CommandHelpTemplate = `USAGE:
   {{if .Usage}}{{.Usage}}{{else}}{{.Name}} {{end}}{{if .Category}}
CATEGORY:
   {{.Category}}{{end}}{{if .Description}}
DESCRIPTION:
   {{.Description}}{{end}}
`
	cli.SubcommandHelpTemplate = `USAGE:
   {{if .Usage}}{{.Usage}}{{else}}{{.Name}} {{end}}

COMMANDS:{{range .VisibleCategories}}{{if .Name}}
   {{.Name}}:{{range .VisibleCommands}}
     {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{else}}{{range .VisibleCommands}}
   {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{end}}{{end}}
`
	cli.AppHelpTemplate = `{{if .VisibleCommands}}COMMANDS:{{range .VisibleCategories}}{{if .Name}}
   {{.Name}}:{{range .VisibleCommands}}
     {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{else}}{{range .VisibleCommands}}
   {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{end}}{{end}}{{if .VisibleFlags}}

GLOBAL OPTIONS:
   {{range $index, $option := .VisibleFlags}}{{if $index}}
   {{end}}{{$option}}{{end}}{{end}}{{if .Copyright}}

COPYRIGHT:
   {{.Copyright}}{{end}}{{if .Version}}
VERSION:
   {{.Version}}{{end}}{{end}}{{if .Description}}

DESCRIPTION:
   {{.Description}}{{end}}{{if len .Authors}}

AUTHOR{{with $length := len .Authors}}{{if ne 1 $length}}S{{end}}{{end}}:
   {{range $index, $author := .Authors}}{{if $index}}
   {{end}}{{$author}}{{end}}{{end}}
`
	app := &cli.App{
		Name:     "xCLI",
		Version:  "v0.99.0",
		Compiled: time.Now(),
		Authors: []*cli.Author{
			&cli.Author{
				Name:  "Kingwel Xie",
				Email: "xie_jw@hisuntech.com",
			},
		},
		//Copyright: "(c) 2020",
		//Usage:     "demonstrate available API",
		//UsageText: "contrive - demonstrating the available API",
		//ArgsUsage: "[args and such]",

		Metadata: map[string]interface{}{
			"readline": rl,
		},

		Commands: cmds,
		//Writer:   rl.Stdout(),
		//Writer:   rl.Stderr(),

		HideHelp:    false,
		HideVersion: false,
		Before: func(c *cli.Context) error {
			//fmt.Fprintf(c.App.Writer, "CMD Begins\n")
			return nil
		},
		After: func(c *cli.Context) error {
			//fmt.Fprintf(c.App.Writer, "CMD Ends!\n")
			return nil
		},
		CommandNotFound: func(c *cli.Context, command string) {
			fmt.Fprintf(c.App.Writer, "No %q here.\n", command)
		},
		OnUsageError: func(c *cli.Context, err error, isSubcommand bool) error {
			if isSubcommand {
				return err
			}

			fmt.Fprintf(c.App.Writer, "WRONG: %#v\n", err)
			return nil
		},
		Action: func(c *cli.Context) error {
			args := c.Args()
			if args.Present() {
				fmt.Fprintf(c.App.Writer, "%s : %s\n", "Unknown command", args.Slice())
			}
			return nil
		},
		ExitErrHandler: func(c *cli.Context, err error) {
			if err != nil {
				fmt.Fprintf(c.App.Writer, "Error : %s\n\n", err)
			}
			if c.Command != nil && err == ErrBadArgument {
				cli.ShowCommandHelp(c, c.Command.Name)
			}
		},
	}

	// callback to set user specific data
	if f != nil {
		f(app.Metadata)
	}
	return app
}
