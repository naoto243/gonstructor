package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/iancoleman/strcase"
	g "github.com/moznion/gowrtr/generator"
	"golang.org/x/tools/go/packages"
)

const (
	allArgsConstructorType = "allArgs"
	builderConstructorType = "builder"
	gonstructorTag         = "gonstructor"
)

var (
	typeName         = flag.String("type", "", "[mandatory] a type name")
	output           = flag.String("output", "", "[optional] output file name; default srcdir/<type>_gen.go")
	constructorTypes = flag.String("constructorTypes", allArgsConstructorType, fmt.Sprintf("[optional] comma-separated list of constructor types; it expects `%s` and `%s`", allArgsConstructorType, builderConstructorType))
)

func main() {
	flag.Parse()

	if *typeName == "" {
		flag.Usage()
		os.Exit(2)
	}

	constructorTypes, err := getConstructorTypes()
	if err != nil {
		log.Printf("[error] %s", err)
		flag.Usage()
		os.Exit(2)
	}

	args := flag.Args()
	if len(args) <= 0 {
		args = []string{"."}
	}

	pkg, err := parsePackage(args)
	if err != nil {
		log.Fatal(err)
	}

	astFiles, err := parseFiles(pkg.GoFiles)
	if err != nil {
		log.Fatal(err)
	}

	fields, err := extractFieldsForConstructorFromASTs(*typeName, astFiles)
	if err != nil {
		log.Fatal(err)
	}

	root := g.NewRoot(
		g.NewComment(fmt.Sprintf(" Code generated by gonstructor %s; DO NOT EDIT.", strings.Join(os.Args[1:], " "))),
		g.NewNewline(),
		g.NewPackage(pkg.Name),
		g.NewNewline(),
	)

	for _, constructorType := range constructorTypes {
		switch constructorType {
		case allArgsConstructorType:
			root = root.AddStatements(generateAllArgsConstructor(*typeName, fields))
		case builderConstructorType:
			root = root.AddStatements(generateBuilderConstructor(*typeName, fields))
		default:
			// unreachable, just in case
			log.Fatalf("unexpected constructor type has come [given=%s]", constructorType)
		}
	}

	code, err := root.EnableGoimports().EnableSyntaxChecking().Generate(0)
	if err != nil {
		log.Fatal(err)
	}

	filenameToGenerate := ""
	if *output == "" {
		var dir string
		if len(args) == 1 && isDirectory(args[0]) {
			dir = args[0]
		} else {
			dir = filepath.Dir(args[0])
		}
		filenameToGenerate = fmt.Sprintf("%s/%s_gen.go", dir, strcase.ToSnake(*typeName))
	} else {
		filenameToGenerate = *output
	}

	err = ioutil.WriteFile(filenameToGenerate, []byte(code), 0644)
	if err != nil {
		log.Fatal(fmt.Errorf("[error] failed output generated code to a file: %w", err))
	}
}

func generateAllArgsConstructor(typeName string, fields []*fieldForConstructor) g.Statement {
	funcSignature := g.NewFuncSignature(fmt.Sprintf("New%s", strcase.ToCamel(typeName)))
	items := make([]string, 0)

	for _, field := range fields {
		if field.shouldIgnore {
			continue
		}
		funcSignature = funcSignature.AddFuncParameters(g.NewFuncParameter(strcase.ToLowerCamel(field.fieldName), field.fieldType))
		items = append(items, fmt.Sprintf("%s: %s", field.fieldName, strcase.ToLowerCamel(field.fieldName)))
	}

	funcSignature = funcSignature.AddReturnTypes("*" + typeName)

	return g.NewFunc(
		nil,
		funcSignature,
		g.NewReturnStatement(fmt.Sprintf("&%s{%s}", typeName, strings.Join(items, ","))),
	)
}

func generateBuilderConstructor(typeName string, fields []*fieldForConstructor) g.Statement {
	builderConstructorName := fmt.Sprintf("New%sBuilder", strcase.ToCamel(typeName))
	builderType := fmt.Sprintf("%sBuilder", strcase.ToCamel(typeName))

	builderConstructorFunc :=
		g.NewFunc(
			nil,
			g.NewFuncSignature(builderConstructorName).AddReturnTypes(fmt.Sprintf("*%s", builderType)),
			g.NewReturnStatement(fmt.Sprintf("&%s{}", builderType)),
		)

	builderStruct := g.NewStruct(builderType)
	builderFieldFuncs := make([]*g.Func, 0)
	items := make([]string, 0)
	for _, field := range fields {
		if field.shouldIgnore {
			continue
		}
		builderStruct = builderStruct.AddField(
			strcase.ToLowerCamel(field.fieldName),
			field.fieldType,
		)

		builderFieldFuncs = append(builderFieldFuncs, g.NewFunc(
			g.NewFuncReceiver("b", "*"+builderType),
			g.NewFuncSignature(strcase.ToCamel(field.fieldName)).
				AddFuncParameters(g.NewFuncParameter(strcase.ToLowerCamel(field.fieldName), field.fieldType)).
				AddReturnTypes("*"+builderType),
			g.NewRawStatement(fmt.Sprintf("b.%s = %s", strcase.ToLowerCamel(field.fieldName), strcase.ToLowerCamel(field.fieldName))),
			g.NewReturnStatement("b"),
		))

		items = append(items, fmt.Sprintf("%s: b.%s", field.fieldName, strcase.ToLowerCamel(field.fieldName)))
	}

	root := g.NewRoot(builderStruct, builderConstructorFunc)
	for _, f := range builderFieldFuncs {
		root = root.AddStatements(f)
	}
	root = root.AddStatements(
		g.NewFunc(
			g.NewFuncReceiver("b", "*"+builderType),
			g.NewFuncSignature("Build").
				AddReturnTypes("*"+typeName),
			g.NewReturnStatement(fmt.Sprintf("&%s{%s}", typeName, strings.Join(items, ","))),
		),
	)

	return root
}

func getConstructorTypes() ([]string, error) {
	typs := strings.Split(*constructorTypes, ",")
	for _, typ := range typs {
		if typ != allArgsConstructorType && typ != builderConstructorType {
			return nil, fmt.Errorf("unexpected constructor type has come [given=%s]", typ)
		}
	}
	return typs, nil
}

var parsedFileCache = make(map[string]*ast.File)

func parseFiles(files []string) ([]*ast.File, error) {
	fset := token.NewFileSet()

	astFiles := make([]*ast.File, len(files))
	for i, file := range files {
		if parsed := parsedFileCache[file]; parsed != nil {
			astFiles[i] = parsed
			continue
		}

		parsed, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("failed to parse file: %w", err)
		}
		astFiles[i] = parsed
		parsedFileCache[file] = parsed
	}
	return astFiles, nil
}

func extractFieldsForConstructorFromASTs(typeName string, astFiles []*ast.File) ([]*fieldForConstructor, error) {
	for _, astFile := range astFiles {
		for _, decl := range astFile.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}

				structName := typeSpec.Name.Name
				if typeName != structName {
					continue
				}

				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}

				return correctFieldsForConstructor(structType.Fields.List), nil
			}
		}
	}

	return nil, fmt.Errorf("there is no suitable struct that matches given typeName [given=%s]", typeName)
}

type fieldForConstructor struct {
	fieldName    string
	fieldType    string
	shouldIgnore bool
}

func correctFieldsForConstructor(fields []*ast.Field) []*fieldForConstructor {
	fs := make([]*fieldForConstructor, 0)
	for _, field := range fields {
		shouldIgnore := false
		if field.Tag != nil && len(field.Tag.Value) >= 1 {
			customTag := reflect.StructTag(field.Tag.Value[1 : len(field.Tag.Value)-1])
			shouldIgnore = customTag.Get(gonstructorTag) == "-"
		}

		fs = append(fs, &fieldForConstructor{
			fieldName:    field.Names[0].Name,
			fieldType:    types.ExprString(field.Type),
			shouldIgnore: shouldIgnore,
		})
	}
	return fs
}

func parsePackage(patterns []string) (*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesSizes |
			packages.NeedSyntax |
			packages.NeedTypesInfo,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("failed to load package: %w", err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("ambiguous error; %d packages found", len(pkgs))
	}
	return pkgs[0], nil
}

func isDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		log.Fatal(err)
	}
	return info.IsDir()
}
