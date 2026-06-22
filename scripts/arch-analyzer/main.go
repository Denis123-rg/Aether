package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileInfo struct {
	Path       string     `json:"path"`
	Package    string     `json:"package"`
	Imports    []string   `json:"imports"`
	Externs    []string   `json:"external_imports"`
	Types      []TypeInfo `json:"types"`
	Functions  []FuncInfo `json:"functions"`
	Interfaces []string   `json:"interfaces"`
	Structs    []string   `json:"structs"`
	Constants  []string   `json:"constants"`
	Variables  []string   `json:"variables"`
	Doc        string     `json:"doc"`
	BuildTags  string     `json:"build_tags,omitempty"`
	Generics   []string   `json:"generic_types"`
}

type TypeInfo struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Methods  []string `json:"methods"`
	Embedded []string `json:"embedded"`
	Generics []string `json:"generics"`
}

type FuncInfo struct {
	Name     string `json:"name"`
	Exported bool   `json:"exported"`
	Receiver string `json:"receiver,omitempty"`
	Params   string `json:"params"`
	Results  string `json:"results"`
	Generic  bool   `json:"generic"`
}

type PackageInfo struct {
	Name  string     `json:"name"`
	Path  string     `json:"path"`
	Files []FileInfo `json:"files"`
}

type ImportEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Depth  int    `json:"depth"`
}

type ImportGraph struct {
	Nodes    []string     `json:"nodes"`
	Edges    []ImportEdge `json:"edges"`
	Circular [][]string   `json:"circular_imports"`
}

type ArchReport struct {
	Packages         []PackageInfo     `json:"packages"`
	ImportGraph      ImportGraph       `json:"import_graph"`
	ForbiddenImports []ForbiddenImport `json:"forbidden_imports"`
	Warnings         []string          `json:"warnings"`
	Stats            Stats             `json:"stats"`
}

type ForbiddenImport struct {
	Source  string `json:"source"`
	Target  string `json:"target"`
	Message string `json:"message"`
}

type Stats struct {
	TotalFiles      int `json:"total_files"`
	TotalPackages   int `json:"total_packages"`
	TotalImports    int `json:"total_imports"`
	TotalFunctions  int `json:"total_functions"`
	TotalTypes      int `json:"total_types"`
	TotalInterfaces int `json:"total_interfaces"`
	TotalStructs    int `json:"total_structs"`
	CircularDeps    int `json:"circular_deps"`
	ForbiddenDeps   int `json:"forbidden_deps"`
}

type LayerRule struct {
	Layer        string   `json:"layer"`
	Packages     []string `json:"packages"`
	CanImport    []string `json:"can_import"`
	CannotImport []string `json:"cannot_import"`
}

var layerRules = []LayerRule{
	{Layer: "handler", Packages: []string{"cmd/executor", "cmd/monitor", "cmd/telebot", "cmd/reconciler", "cmd/signer"}, CanImport: []string{"internal", "github.com/aether-arb/aether/internal"}, CannotImport: []string{}},
	{Layer: "service", Packages: []string{"internal/risk", "internal/strategy", "internal/events", "internal/metrics", "internal/grpc", "internal/signer", "internal/tracing"}, CanImport: []string{"internal/config", "internal/pb", "github.com/aether-arb/aether/internal"}, CannotImport: []string{"cmd", "github.com/aether-arb/aether/cmd"}},
	{Layer: "db", Packages: []string{"internal/db"}, CanImport: []string{"internal/config", "github.com/aether-arb/aether/internal/config"}, CannotImport: []string{"cmd", "github.com/aether-arb/aether/cmd", "internal/grpc", "internal/risk", "internal/strategy", "internal/events", "internal/signer", "internal/tracing"}},
	{Layer: "config", Packages: []string{"internal/config"}, CanImport: nil, CannotImport: []string{"internal", "github.com/aether-arb/aether/internal", "cmd", "github.com/aether-arb/aether/cmd"}},
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	output := "arch-report.json"
	if len(os.Args) > 2 {
		output = os.Args[2]
	}

	report := analyze(root)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling report: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(output, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Architecture report written to %s\n", output)
	fmt.Printf("Stats: %d files, %d packages, %d imports, %d functions, %d types\n",
		report.Stats.TotalFiles, report.Stats.TotalPackages, report.Stats.TotalImports,
		report.Stats.TotalFunctions, report.Stats.TotalTypes)
	if report.Stats.CircularDeps > 0 {
		fmt.Printf("WARNING: %d circular dependencies detected!\n", report.Stats.CircularDeps)
	}
	if report.Stats.ForbiddenDeps > 0 {
		fmt.Printf("WARNING: %d forbidden dependencies detected!\n", report.Stats.ForbiddenDeps)
		os.Exit(1)
	}
	os.Exit(report.Stats.ForbiddenDeps)
}

var projectModule = "github.com/aether-arb/aether"

func analyze(root string) ArchReport {
	report := ArchReport{}
	pkgMap := make(map[string]*PackageInfo)
	fset := token.NewFileSet()
	importMap := make(map[string]map[string]bool)

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, ".git/") || strings.Contains(path, "target/") {
			return nil
		}

		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil
		}

		pkgPath := filepath.Dir(path)
		pkgName := f.Name.Name

		if _, ok := pkgMap[pkgPath]; !ok {
			pkgMap[pkgPath] = &PackageInfo{Name: pkgName, Path: pkgPath}
		}

		finfo := FileInfo{
			Path:    path,
			Package: pkgName,
		}

		if f.Doc != nil {
			finfo.Doc = f.Doc.Text()
		}

		for _, spec := range f.Imports {
			if spec.Path != nil {
				imp := strings.Trim(spec.Path.Value, "\"")
				finfo.Imports = append(finfo.Imports, imp)
				if !strings.HasPrefix(imp, projectModule) && !strings.Contains(imp, ".") {
					continue
				}
				if strings.HasPrefix(imp, projectModule) {
					rel := strings.TrimPrefix(imp, projectModule+"/")
					if importMap[pkgPath] == nil {
						importMap[pkgPath] = make(map[string]bool)
					}
					importMap[pkgPath][rel] = true
				}
			}
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.GenDecl:
				for _, spec := range x.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						ti := TypeInfo{Name: s.Name.Name}
						switch s.Type.(type) {
						case *ast.StructType:
							ti.Kind = "struct"
							finfo.Structs = append(finfo.Structs, s.Name.Name)
							if st, ok := s.Type.(*ast.StructType); ok {
								for _, fld := range st.Fields.List {
									if sel, ok := fld.Type.(*ast.SelectorExpr); ok {
										ti.Embedded = append(ti.Embedded, fmt.Sprintf("%s.%s", sel.X, sel.Sel.Name))
									} else if ident, ok := fld.Type.(*ast.Ident); ok {
										if len(fld.Names) == 0 {
											ti.Embedded = append(ti.Embedded, ident.Name)
										}
									}
								}
							}
						case *ast.InterfaceType:
							ti.Kind = "interface"
							finfo.Interfaces = append(finfo.Interfaces, s.Name.Name)
							if it, ok := s.Type.(*ast.InterfaceType); ok {
								for _, m := range it.Methods.List {
									if sel, ok := m.Type.(*ast.SelectorExpr); ok {
										ti.Embedded = append(ti.Embedded, fmt.Sprintf("%s.%s", sel.X, sel.Sel.Name))
									} else if ident, ok := m.Type.(*ast.Ident); ok {
										ti.Embedded = append(ti.Embedded, ident.Name)
									}
								}
							}
						case *ast.MapType:
							ti.Kind = "map"
						case *ast.ArrayType:
							ti.Kind = "slice"
						default:
							if s.TypeParams != nil {
								ti.Kind = "generic_type"
								for _, tp := range s.TypeParams.List {
									for _, name := range tp.Names {
										ti.Generics = append(ti.Generics, name.Name)
									}
								}
							} else {
								ti.Kind = "alias"
							}
						}
						if s.TypeParams != nil && ti.Kind != "generic_type" {
							for _, tp := range s.TypeParams.List {
								for _, name := range tp.Names {
									ti.Generics = append(ti.Generics, name.Name)
								}
							}
						}
						finfo.Types = append(finfo.Types, ti)

					case *ast.ValueSpec:
						for _, name := range s.Names {
							if s.Type != nil {
								if _, ok := s.Type.(*ast.FuncType); !ok {
									if name.IsExported() {
										finfo.Constants = append(finfo.Constants, name.Name)
									}
								}
							}
							if len(s.Values) > 0 {
								if name.IsExported() {
									finfo.Variables = append(finfo.Variables, name.Name)
								}
							}
						}
					}
				}

			case *ast.FuncDecl:
				fi := FuncInfo{
					Name:     x.Name.Name,
					Exported: x.Name.IsExported(),
				}
				if x.Recv != nil && len(x.Recv.List) > 0 {
					recvType := x.Recv.List[0].Type
					switch t := recvType.(type) {
					case *ast.Ident:
						fi.Receiver = t.Name
					case *ast.StarExpr:
						if ident, ok := t.X.(*ast.Ident); ok {
							fi.Receiver = "*" + ident.Name
						}
					}
				}
				fi.Generic = x.Type.TypeParams != nil
				finfo.Functions = append(finfo.Functions, fi)
			}
			return true
		})

		pkgMap[pkgPath].Files = append(pkgMap[pkgPath].Files, finfo)
		return nil
	})

	for _, pkg := range pkgMap {
		report.Packages = append(report.Packages, *pkg)
	}
	sort.Slice(report.Packages, func(i, j int) bool {
		return report.Packages[i].Path < report.Packages[j].Path
	})

	allPkgPaths := make([]string, 0, len(pkgMap))
	for p := range pkgMap {
		allPkgPaths = append(allPkgPaths, p)
	}
	sort.Strings(allPkgPaths)

	graph := ImportGraph{Nodes: allPkgPaths}
	for src, targets := range importMap {
		for tgt := range targets {
			graph.Edges = append(graph.Edges, ImportEdge{Source: src, Target: tgt, Depth: 1})
		}
	}

	graph.Circular = detectCycles(allPkgPaths, importMap)

	for _, pkg := range report.Packages {
		for fiIdx := range pkg.Files {
			f := &pkg.Files[fiIdx]
			for _, imp := range f.Imports {
				if strings.HasPrefix(imp, projectModule) {
					rel := strings.TrimPrefix(imp, projectModule+"/")
					hasLocal := false
					for _, lp := range allPkgPaths {
						if lp == rel || strings.HasPrefix(lp, rel+"/") || strings.HasPrefix(rel, lp+"/") {
							hasLocal = true
							break
						}
					}
					if !hasLocal {
						f.Externs = append(f.Externs, imp)
					}
				} else {
					f.Externs = append(f.Externs, imp)
				}
			}
		}
	}

	for _, pkg := range report.Packages {
		for _, f := range pkg.Files {
			for _, imp := range f.Imports {
				if strings.HasPrefix(imp, projectModule) {
					rel := strings.TrimPrefix(imp, projectModule+"/")
					for _, rule := range layerRules {
						if isPackageInLayer(pkg.Path, rule.Packages) {
							for _, forbidden := range rule.CannotImport {
								if strings.HasPrefix(rel, forbidden) || strings.HasPrefix(forbidden, rel) {
									fi := ForbiddenImport{
										Source:  pkg.Path,
										Target:  imp,
										Message: fmt.Sprintf("Layer violation: %s (layer: %s) must not import %s", pkg.Path, rule.Layer, imp),
									}
									report.ForbiddenImports = append(report.ForbiddenImports, fi)
								}
							}
						}
					}
				}
			}
		}
	}

	for _, pkg := range report.Packages {
		report.Stats.TotalFiles += len(pkg.Files)
		for _, f := range pkg.Files {
			report.Stats.TotalImports += len(f.Imports)
			report.Stats.TotalFunctions += len(f.Functions)
			report.Stats.TotalTypes += len(f.Types)
			report.Stats.TotalInterfaces += len(f.Interfaces)
			report.Stats.TotalStructs += len(f.Structs)
		}
	}
	report.Stats.TotalPackages = len(report.Packages)
	report.Stats.CircularDeps = len(graph.Circular)
	report.Stats.ForbiddenDeps = len(report.ForbiddenImports)
	report.ImportGraph = graph

	if len(report.ForbiddenImports) > 0 {
		for _, fi := range report.ForbiddenImports {
			report.Warnings = append(report.Warnings, fi.Message)
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", fi.Message)
		}
	}

	return report
}

func isPackageInLayer(pkgPath string, layerPkgs []string) bool {
	for _, lp := range layerPkgs {
		if pkgPath == lp || strings.HasPrefix(pkgPath, lp+"/") || strings.HasPrefix(pkgPath, lp) {
			return true
		}
	}
	return false
}

func detectCycles(pkgs []string, importMap map[string]map[string]bool) [][]string {
	var cycles [][]string
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := make([]string, 0)

	var dfs func(node string)
	dfs = func(node string) {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)

		if targets, ok := importMap[node]; ok {
			for tgt := range targets {
				if !visited[tgt] {
					dfs(tgt)
				} else if recStack[tgt] {
					cycle := make([]string, 0)
					started := false
					for _, p := range path {
						if p == tgt || started {
							started = true
							cycle = append(cycle, p)
						}
					}
					if len(cycle) > 1 {
						cycles = append(cycles, cycle)
					}
				}
			}
		}

		path = path[:len(path)-1]
		recStack[node] = false
	}

	for _, pkg := range pkgs {
		if !visited[pkg] {
			dfs(pkg)
		}
	}
	return cycles
}
