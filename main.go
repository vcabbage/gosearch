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

func run() int {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage of gosearch:
gosearch [flags] [search term]
`)
		flag.PrintDefaults()
	}
	var (
		limit         = flag.Int("limit", 10, "maximum number of search results to display")
		forks         = flag.Bool("forks", false, "include forks")
		apps          = flag.Bool("apps", false, "search for main packages instead of libs")
		minStars      = flag.Int("stars", 1, "minimum # of stars for package to be displayed")
		minImports    = flag.Int("imports", 0, "minimum # of imports for package to be displayed")
		showInstalled = flag.Bool("installed", false, "mark packeges that are already installed with *")
		inPath        = flag.Bool("inpath", true, "search term must be in package path")
		goflags       []string
	)
	flag.Var((*stringsFlag)(&goflags), "goflags", `arguments to be passed to "go get" (default "-u -v")`)
	flag.Parse()

	if flag.Arg(0) == "" {
		fmt.Fprintf(os.Stderr, "Must provide search term.")
		flag.PrintDefaults()
		return 1
	}
	query := flag.Arg(0)

	if goflags == nil {
		goflags = []string{"-u", "-v"}
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
	args = append(args, goflags...)
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
	ImportCount int     `bool:"import_count"`
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

// stringsFlag copied from https://github.com/golang/go/blob/4e584c52036fb2a572fab466e2a291fb695da882/src/cmd/go/build.go
// Copyright 2011 The Go Authors. All rights reserved.
type stringsFlag []string

func (v *stringsFlag) Set(s string) error {
	var err error
	*v, err = splitQuotedFields(s)
	if *v == nil {
		*v = []string{}
	}
	return err
}

func splitQuotedFields(s string) ([]string, error) {
	// Split fields allowing '' or "" around elements.
	// Quotes further inside the string do not count.
	var f []string
	for len(s) > 0 {
		for len(s) > 0 && isSpaceByte(s[0]) {
			s = s[1:]
		}
		if len(s) == 0 {
			break
		}
		// Accepted quoted string. No unescaping inside.
		if s[0] == '"' || s[0] == '\'' {
			quote := s[0]
			s = s[1:]
			i := 0
			for i < len(s) && s[i] != quote {
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated %c string", quote)
			}
			f = append(f, s[:i])
			s = s[i+1:]
			continue
		}
		i := 0
		for i < len(s) && !isSpaceByte(s[i]) {
			i++
		}
		f = append(f, s[:i])
		s = s[i:]
	}
	return f, nil
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func (v *stringsFlag) String() string {
	return "<stringsFlag>"
}
