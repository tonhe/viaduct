//go:build ignore

// Mutation tester for github.com/tonhe/viaduct.
// Run: go run scripts/mutate/main.go [flags] <package>...
//
// Uses go/parser + go/ast + go/printer to inject mutations, run tests,
// and restore the original. No external tools required.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mutation represents a single code change candidate.
type Mutation struct {
	File        string
	Line        int
	Col         int
	Description string
	Apply       func(src []byte) ([]byte, bool) // returns mutated src, or false if not applicable
}

// Result is the outcome of running tests against a mutant.
type Result struct {
	Mutation Mutation
	Killed   bool   // true = tests caught the mutation (good)
	Reason   string // exit status or "timeout"
}

// PkgResult is all results for one package.
type PkgResult struct {
	Pkg      string
	Results  []Result
	Killed   int
	Survived int
}

var (
	flagMaxMutants = flag.Int("max", 50, "max mutants to test per package")
	flagTimeout    = flag.String("timeout", "30s", "test timeout per mutant")
	flagVerbose    = flag.Bool("v", false, "verbose output per mutation")
)

func main() {
	flag.Parse()
	pkgs := flag.Args()
	if len(pkgs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/mutate/main.go [flags] <package>...")
		os.Exit(1)
	}

	var allResults []PkgResult
	for _, pkg := range pkgs {
		fmt.Printf("\n=== Mutating %s ===\n", pkg)
		r := mutatePkg(pkg)
		allResults = append(allResults, r)
		printPkgSummary(r)
	}

	printOverallSummary(allResults)
}

// mutatePkg finds all non-test Go files in a package directory,
// generates mutations, applies each, runs tests, restores.
func mutatePkg(pkg string) PkgResult {
	pr := PkgResult{Pkg: pkg}

	// Resolve package directory from import path.
	dir, err := pkgDir(pkg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve %s: %v\n", pkg, err)
		return pr
	}

	// Parse all non-test .go files.
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no .go files in %s\n", dir)
		return pr
	}

	var mutations []Mutation
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		ms, err := findMutations(fset, file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", file, err)
			continue
		}
		mutations = append(mutations, ms...)
	}

	if len(mutations) == 0 {
		fmt.Printf("  no mutations found\n")
		return pr
	}

	// Cap to --max.
	max := *flagMaxMutants
	if max > 0 && len(mutations) > max {
		// Sample evenly across the mutation list.
		sampled := make([]Mutation, 0, max)
		step := float64(len(mutations)) / float64(max)
		for i := 0; i < max; i++ {
			idx := int(float64(i) * step)
			if idx >= len(mutations) {
				idx = len(mutations) - 1
			}
			sampled = append(sampled, mutations[idx])
		}
		mutations = sampled
	}

	fmt.Printf("  Testing %d mutation(s) (capped at %d)...\n", len(mutations), max)

	for i, m := range mutations {
		if *flagVerbose {
			fmt.Printf("  [%d/%d] %s:%d — %s\n", i+1, len(mutations), filepath.Base(m.File), m.Line, m.Description)
		}

		// Read original.
		orig, err := os.ReadFile(m.File)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  read %s: %v\n", m.File, err)
			continue
		}

		// Apply mutation.
		mutated, ok := m.Apply(orig)
		if !ok {
			if *flagVerbose {
				fmt.Printf("  [%d/%d] SKIP (apply failed)\n", i+1, len(mutations))
			}
			continue
		}

		// Write mutant.
		if err := os.WriteFile(m.File, mutated, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "  write %s: %v\n", m.File, err)
			// Restore just in case.
			_ = os.WriteFile(m.File, orig, 0644)
			continue
		}

		// Run tests.
		killed, reason := runTests(pkg)

		// Always restore.
		if err := os.WriteFile(m.File, orig, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "  FATAL: cannot restore %s: %v\n", m.File, err)
			fmt.Fprintf(os.Stderr, "  Original content saved in /tmp/mutate_backup.go\n")
			_ = os.WriteFile("/tmp/mutate_backup.go", orig, 0644)
			os.Exit(2)
		}

		r := Result{Mutation: m, Killed: killed, Reason: reason}
		pr.Results = append(pr.Results, r)
		if killed {
			pr.Killed++
			if *flagVerbose {
				fmt.Printf("  [%d/%d] KILLED — %s\n", i+1, len(mutations), m.Description)
			} else {
				fmt.Print(".")
			}
		} else {
			pr.Survived++
			if !*flagVerbose {
				fmt.Printf("\n  SURVIVED: %s:%d — %s\n", filepath.Base(m.File), m.Line, m.Description)
			} else {
				fmt.Printf("  [%d/%d] SURVIVED — %s:%d — %s\n", i+1, len(mutations), filepath.Base(m.File), m.Line, m.Description)
			}
		}
	}
	if !*flagVerbose {
		fmt.Println()
	}
	return pr
}

// runTests runs the test suite for the given package, returning killed=true
// when the tests fail (mutation caught).
func runTests(pkg string) (killed bool, reason string) {
	timeout := *flagTimeout
	cmd := exec.Command("go", "test", "-timeout", timeout, pkg)
	cmd.Dir = moduleRoot()

	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return false, "tests passed (mutant survived)"
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return true, fmt.Sprintf("exit %d", exitErr.ExitCode())
	}
	// timeout or other error — treat as killed
	return true, err.Error()
}

// findMutations parses a Go file and returns the list of possible mutations.
func findMutations(fset *token.FileSet, filename string) ([]Mutation, error) {
	src, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}

	var mutations []Mutation
	addMut := func(pos token.Pos, desc string, apply func([]byte) ([]byte, bool)) {
		position := fset.Position(pos)
		mutations = append(mutations, Mutation{
			File:        filename,
			Line:        position.Line,
			Col:         position.Column,
			Description: desc,
			Apply:       apply,
		})
	}

	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		switch node := n.(type) {
		// Binary expression mutations: comparisons and logical operators
		case *ast.BinaryExpr:
			pos := node.OpPos
			switch node.Op {
			case token.LSS: // <  → <=
				addMut(pos, "< → <=", replaceToken(fset, src, pos, token.LSS, "<", "<="))
			case token.LEQ: // <= → <
				addMut(pos, "<= → <", replaceToken(fset, src, pos, token.LEQ, "<=", "<"))
			case token.GTR: // >  → >=
				addMut(pos, "> → >=", replaceToken(fset, src, pos, token.GTR, ">", ">="))
			case token.GEQ: // >= → >
				addMut(pos, ">= → >", replaceToken(fset, src, pos, token.GEQ, ">=", ">"))
			case token.EQL: // == → !=
				addMut(pos, "== → !=", replaceToken(fset, src, pos, token.EQL, "==", "!="))
			case token.NEQ: // != → ==
				addMut(pos, "!= → ==", replaceToken(fset, src, pos, token.NEQ, "!=", "=="))
			case token.LAND: // && → ||
				addMut(pos, "&& → ||", replaceToken(fset, src, pos, token.LAND, "&&", "||"))
			case token.LOR: // || → &&
				addMut(pos, "|| → &&", replaceToken(fset, src, pos, token.LOR, "||", "&&"))
			case token.ADD: // + → -
				addMut(pos, "+ → -", replaceToken(fset, src, pos, token.ADD, "+", "-"))
			case token.SUB: // - → +
				addMut(pos, "- → +", replaceToken(fset, src, pos, token.SUB, "-", "+"))
			}

		// Unary NOT deletion: !x → x
		case *ast.UnaryExpr:
			if node.Op == token.NOT {
				pos := node.OpPos
				addMut(pos, "! deleted", deleteUnaryNot(fset, src, pos))
			}

		// Return statements: return true → return false, return false → return true
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if ident, ok := result.(*ast.Ident); ok {
					switch ident.Name {
					case "true":
						addMut(ident.Pos(), "return true → return false",
							replaceIdentAt(fset, src, ident.Pos(), "true", "false"))
					case "false":
						addMut(ident.Pos(), "return false → return true",
							replaceIdentAt(fset, src, ident.Pos(), "false", "true"))
					}
				}
			}

		// Integer literal off-by-one: N → N+1 and N → N-1 (for small N like 0,1,2)
		case *ast.BasicLit:
			if node.Kind == token.INT {
				n, err := strconv.Atoi(node.Value)
				if err == nil && n >= 0 && n <= 5 {
					pos := node.Pos()
					orig := node.Value
					addMut(pos, fmt.Sprintf("int %s → %d", orig, n+1),
						replaceBasicLit(fset, src, pos, orig, strconv.Itoa(n+1)))
					if n > 0 {
						addMut(pos, fmt.Sprintf("int %s → %d", orig, n-1),
							replaceBasicLit(fset, src, pos, orig, strconv.Itoa(n-1)))
					}
				}
			}

		// if-body removal: delete the entire if body
		case *ast.IfStmt:
			pos := node.Body.Lbrace
			addMut(pos, "if-body removed", removeIfBody(fset, src, node))
		}
		return true
	})

	return mutations, nil
}

// replaceToken returns an apply function that swaps oldTok for newStr at the
// given source position. We find the token by byte offset.
func replaceToken(fset *token.FileSet, src []byte, pos token.Pos, _ token.Token, oldStr, newStr string) func([]byte) ([]byte, bool) {
	offset := fset.Position(pos).Offset
	return func(current []byte) ([]byte, bool) {
		// Verify the expected text is still at that offset.
		if offset >= len(current) {
			return nil, false
		}
		if !bytes.HasPrefix(current[offset:], []byte(oldStr)) {
			return nil, false
		}
		result := make([]byte, 0, len(current)+len(newStr)-len(oldStr))
		result = append(result, current[:offset]...)
		result = append(result, []byte(newStr)...)
		result = append(result, current[offset+len(oldStr):]...)
		// Quick syntax check.
		if _, err := parser.ParseFile(token.NewFileSet(), "", result, 0); err != nil {
			return nil, false
		}
		return result, true
	}
}

// replaceIdentAt replaces an identifier at a position.
func replaceIdentAt(fset *token.FileSet, _ []byte, pos token.Pos, oldStr, newStr string) func([]byte) ([]byte, bool) {
	offset := fset.Position(pos).Offset
	return func(current []byte) ([]byte, bool) {
		if offset >= len(current) {
			return nil, false
		}
		if !bytes.HasPrefix(current[offset:], []byte(oldStr)) {
			return nil, false
		}
		result := make([]byte, 0, len(current))
		result = append(result, current[:offset]...)
		result = append(result, []byte(newStr)...)
		result = append(result, current[offset+len(oldStr):]...)
		if _, err := parser.ParseFile(token.NewFileSet(), "", result, 0); err != nil {
			return nil, false
		}
		return result, true
	}
}

// replaceBasicLit replaces a literal value at a position.
func replaceBasicLit(fset *token.FileSet, _ []byte, pos token.Pos, oldStr, newStr string) func([]byte) ([]byte, bool) {
	return replaceIdentAt(fset, nil, pos, oldStr, newStr)
}

// deleteUnaryNot removes the leading '!' character at pos.
func deleteUnaryNot(fset *token.FileSet, _ []byte, pos token.Pos) func([]byte) ([]byte, bool) {
	offset := fset.Position(pos).Offset
	return func(current []byte) ([]byte, bool) {
		if offset >= len(current) || current[offset] != '!' {
			return nil, false
		}
		result := make([]byte, 0, len(current)-1)
		result = append(result, current[:offset]...)
		result = append(result, current[offset+1:]...)
		if _, err := parser.ParseFile(token.NewFileSet(), "", result, 0); err != nil {
			return nil, false
		}
		return result, true
	}
}

// removeIfBody replaces the body of an if statement with an empty block {}.
func removeIfBody(fset *token.FileSet, origSrc []byte, ifStmt *ast.IfStmt) func([]byte) ([]byte, bool) {
	lbrace := fset.Position(ifStmt.Body.Lbrace).Offset
	rbrace := fset.Position(ifStmt.Body.Rbrace).Offset
	return func(current []byte) ([]byte, bool) {
		if rbrace >= len(current) || lbrace >= rbrace {
			return nil, false
		}
		// Replace body content with empty block.
		result := make([]byte, 0, len(current))
		result = append(result, current[:lbrace+1]...)
		result = append(result, current[rbrace:]...)
		if _, err := parser.ParseFile(token.NewFileSet(), "", result, 0); err != nil {
			return nil, false
		}
		return result, true
	}
}

// pkgDir resolves a Go import path to a directory on disk.
func pkgDir(pkg string) (string, error) {
	cmd := exec.Command("go", "list", "-f", "{{.Dir}}", pkg)
	cmd.Dir = moduleRoot()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// moduleRoot returns the directory containing go.mod.
func moduleRoot() string {
	cmd := exec.Command("go", "env", "GOMODCACHE")
	// We actually want the module root, not the cache. Use `go env GOMOD`.
	cmd2 := exec.Command("go", "env", "GOMOD")
	out, err := cmd2.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		// Fallback to cwd.
		wd, _ := os.Getwd()
		return wd
	}
	_ = cmd
	return filepath.Dir(strings.TrimSpace(string(out)))
}

// printPkgSummary prints results for one package.
func printPkgSummary(pr PkgResult) {
	total := pr.Killed + pr.Survived
	if total == 0 {
		fmt.Printf("  [%s] No mutants tested.\n", pr.Pkg)
		return
	}
	killRate := float64(pr.Killed) / float64(total) * 100
	fmt.Printf("\n--- %s ---\n", pr.Pkg)
	fmt.Printf("  Mutants tested: %d | Killed: %d | Survived: %d | Kill rate: %.1f%%\n",
		total, pr.Killed, pr.Survived, killRate)

	if pr.Survived > 0 {
		fmt.Println("  Surviving mutants:")
		for _, r := range pr.Results {
			if !r.Killed {
				fmt.Printf("    %s:%d — %s\n", filepath.Base(r.Mutation.File), r.Mutation.Line, r.Mutation.Description)
			}
		}
	}
}

// printOverallSummary prints the aggregate across all packages.
func printOverallSummary(all []PkgResult) {
	fmt.Printf("\n\n=== OVERALL MUTATION TESTING SUMMARY ===\n")
	fmt.Printf("%-45s %8s %7s %8s %9s\n", "Package", "Mutants", "Killed", "Survived", "Kill rate")
	fmt.Printf("%s\n", strings.Repeat("-", 80))

	totalM, totalK, totalS := 0, 0, 0
	var concerns []string

	// Sort by package name for stable output.
	sort.Slice(all, func(i, j int) bool { return all[i].Pkg < all[j].Pkg })

	for _, pr := range all {
		total := pr.Killed + pr.Survived
		totalM += total
		totalK += pr.Killed
		totalS += pr.Survived
		if total == 0 {
			fmt.Printf("%-45s %8s\n", shortPkg(pr.Pkg), "no mutants")
			continue
		}
		kr := float64(pr.Killed) / float64(total) * 100
		flag := ""
		if kr < 50 {
			flag = " *** LOW ***"
			concerns = append(concerns, fmt.Sprintf("%s: kill rate %.1f%% < 50%%", shortPkg(pr.Pkg), kr))
		}
		fmt.Printf("%-45s %8d %7d %8d %8.1f%%%s\n",
			shortPkg(pr.Pkg), total, pr.Killed, pr.Survived, kr, flag)
	}

	fmt.Printf("%s\n", strings.Repeat("-", 80))
	overallKR := 0.0
	if totalM > 0 {
		overallKR = float64(totalK) / float64(totalM) * 100
	}
	fmt.Printf("%-45s %8d %7d %8d %8.1f%%\n", "TOTAL", totalM, totalK, totalS, overallKR)

	if len(concerns) > 0 {
		fmt.Println("\nCONCERNS (kill rate < 50%):")
		for _, c := range concerns {
			fmt.Println("  !", c)
		}
	}

	fmt.Printf("\nCompleted at %s\n", time.Now().Format("2006-01-02 15:04:05"))
}

// shortPkg strips the module prefix for display.
func shortPkg(pkg string) string {
	parts := strings.SplitN(pkg, "/", 3)
	if len(parts) == 3 {
		return ".../" + parts[2]
	}
	return pkg
}

// formatSrc formats Go source via go/format (used for verifying mutants parse).
func formatSrc(src []byte) ([]byte, error) {
	return format.Source(src)
}

var _ = formatSrc // avoid unused import warning
