package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

func main() {
	os.Exit(run())
}

func parseArgs(flagset *flag.FlagSet) (string, []string, error) {
	if len(os.Args) > 2 {
		return "", nil, errors.New("Must provide search term")
	}
	query := os.Args[len(os.Args)-1]
	args := os.Args[1 : len(os.Args)-1]

	var localArgs, goArgs []string
	lastLocal := false
	for _, arg := range args {
		norm := strings.TrimPrefix(arg, "-")
		if i := strings.Index(norm, "="); i > -1 {
			norm = norm[:i]
		}
		if flagset.Lookup(norm) != nil || (lastLocal && arg[0] != '-') {
			localArgs = append(localArgs, arg)
			lastLocal = true
			continue
		}
		goArgs = append(goArgs, arg)
		lastLocal = false
	}

	return query, goArgs, flagset.Parse(localArgs)
}

func run() int {
	flagset := flag.NewFlagSet("gosearch", flag.ExitOnError)
	flagset.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage of gosearch:
gosearch [flags] [go get flags] [search term]
`)
		flagset.PrintDefaults()
	}
	var (
		limit = flagset.Int("limit", 10, "maximum number of search results to display")
		forks = flagset.Bool("forks", false, "include forks")
		// libs          = flagset.Bool("libs", true, "include non-main packages")
		apps          = flagset.Bool("apps", false, "search for main packages instead of libs")
		minStars      = flagset.Int("minstars", 1, "minimum # of stars for package to be displayed")
		minImports    = flagset.Int("minimports", 0, "minimum # of imports for package to be displayed")
		showInstalled = flagset.Bool("installed", false, "mark packeges that are already installed with *")
		inPath        = flagset.Bool("inpath", true, "search term must be in package path")
		help          = flagset.Bool("h", false, "display help")
	)
	query, goArgs, err := parseArgs(flagset)
	if err != nil {
		fmt.Println(err)
		fmt.Println()
		flagset.Usage()
		return 1
	}
	if *help {
		flagset.Usage()
		return 1
	}

	matches, err := queryGodoc(query)
	if err != nil {
		fmt.Println(err)
		return 1
	}

	switch {
	case !*apps:
		sort.Sort(packsByImports(matches))
	default:
		sort.Sort(packsByStars(matches))
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "#\tStars\tImports\tPath\tDescription\t\n")
	var packs []pack
	var i int
	for _, p := range matches {
		if !*forks && p.Fork {
			continue
		}
		if *apps && p.Name != "main" {
			continue
		}
		if !*apps && p.Name == "main" {
			continue
		}
		if p.Stars < *minStars {
			continue
		}
		if p.ImportCount < *minImports {
			continue
		}
		if *inPath && !strings.Contains(p.Path, query) {
			continue
		}
		i++

		path := p.Path
		if *showInstalled && exec.Command("go", "list", path).Run() == nil {
			path = "*" + path
		}

		fmt.Fprintf(w, "%d\t%d\t%d\t%s\t%s\t\n", i, p.Stars, p.ImportCount, path, p.Synopsis)
		packs = append(packs, p)
		if *limit > 0 && len(packs) >= *limit {
			break
		}
	}
	w.Flush()
	if *showInstalled {
		fmt.Println("* = installed")
	}

	if len(packs) == 0 {
		fmt.Println("No matches.")
		return 1
	}

	reader := bufio.NewReader(os.Stdin)
	var packN int
	for {
		fmt.Print("Install Package #: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("error reading from stdin")
			continue
		}
		input = strings.TrimSpace(input)
		packN, err = strconv.Atoi(input)
		if err != nil {
			return 0
		}
		if packN < 1 || packN > len(packs) {
			fmt.Println("No entry for", packN)
			continue
		}

		break
	}

	importPath := packs[packN-1].Path

	fmt.Println("Installing", importPath)

	goBin, err := exec.LookPath("go")
	if err != nil {
		fmt.Println("Could not find go binary in PATH")
		return 1
	}

	args := []string{"get"}
	args = append(args, goArgs...)
	args = append(args, importPath)

	fmt.Println("Install command:", goBin, strings.Join(args, " "))
	fmt.Println("Press enter to continue...")
	reader.ReadString('\n')

	cmd := exec.Command(goBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Println(err)
		return 1
	}

	return 0
}

type pack struct {
	Name        string  `json:"name,omitempty"`
	Path        string  `json:"path"`
	ImportCount int     `json:"import_count` // This is verbatim from the gddo repe.
	Synopsis    string  `json:"synopsis,omitempty"`
	Fork        bool    `json:"fork,omitempty"`
	Stars       int     `json:"stars,omitempty"`
	Score       float64 `json:"score,omitempty"`
}

type packsByStars []pack

func (p packsByStars) Len() int           { return len(p) }
func (p packsByStars) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p packsByStars) Less(i, j int) bool { return p[i].Stars > p[j].Stars }

type packsByImports []pack

func (p packsByImports) Len() int           { return len(p) }
func (p packsByImports) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }
func (p packsByImports) Less(i, j int) bool { return p[i].ImportCount > p[j].ImportCount }

func queryGodoc(q string) ([]pack, error) {
	resp, err := http.Get("https://api.godoc.org/search?q=" + url.QueryEscape(q))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, errors.New("godoc: " + resp.Status)
	}

	var results struct{ Results []pack }
	err = json.NewDecoder(resp.Body).Decode(&results)
	if err != nil {
		return nil, err
	}

	return results.Results, nil
}
