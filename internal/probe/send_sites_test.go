package probe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// Every probe write goes through Tracer.send, which sets the write deadline
// (viaduct#115). This test reads the package source and fails when a socket
// write appears anywhere else: a new send path that calls WriteTo directly
// has no deadline and can block the round loop again.
func TestSendPath_EveryWriteGoesThroughSend(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	// Where a write call may be: the one guarded call in send, and the two
	// senders handing it to their socket.
	allowed := map[string]bool{
		"send.go:Tracer.send":         true,
		"probe.go:icmpSender.WriteTo": true,
		"probe.go:rawSender.WriteTo":  true,
	}
	writeCalls := map[string]bool{"WriteTo": true, "Write": true, "WriteMsgIP": true, "WriteToIP": true, "Sendto": true}

	var found []string
	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				where := fname + ":" + funcName(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || !writeCalls[sel.Sel.Name] {
						return true
					}
					found = append(found, where)
					if !allowed[where] {
						t.Errorf("%s: %s.%s called outside Tracer.send: this write has no deadline",
							fset.Position(call.Pos()), exprString(sel.X), sel.Sel.Name)
					}
					return true
				})
			}
		}
	}
	sort.Strings(found)
	want := []string{"probe.go:icmpSender.WriteTo", "probe.go:rawSender.WriteTo", "send.go:Tracer.send"}
	if strings.Join(found, " ") != strings.Join(want, " ") {
		t.Errorf("write call sites = %v, want exactly %v", found, want)
	}
}

// funcName is "Receiver.Method" for a method and "Func" for a function.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	return exprString(typ) + "." + fn.Name.Name
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	default:
		return "?"
	}
}
