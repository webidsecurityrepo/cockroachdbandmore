// Copyright 2023 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var (
	lineComment                                                             bool
	output, typeName, trimPrefix, stringToValueMapName, enumValuesSliceName string
	allowedIntegerTypes                                                     = []string{
		"byte",
		"int",
		"int8",
		"int16",
		"int32",
		"int64",
		"rune",
		"uint",
		"uint8",
		"uint16",
		"uint32",
		"uint64",
	}
)

type tinyStringer struct {
	files                                                                   []string
	typeName, trimPrefix, output, stringToValueMapName, enumValuesSliceName string
	lineComment                                                             bool
}

func init() {
	flag.StringVar(&stringToValueMapName, "stringtovaluemapname", "", "if set, also create a map of enum name -> value of the given name")
	flag.StringVar(&enumValuesSliceName, "enumvaluesslicename", "", "if set, also create a slice of all enum values of the given name")
	flag.StringVar(&output, "output", "", "name of output file; default srcdir/<type>_string.go")
	flag.StringVar(&typeName, "type", "", "the type for which to generate output")
	flag.StringVar(&trimPrefix, "trimprefix", "", "trim the given prefix from generated names")
	flag.BoolVar(&lineComment, "linecomment", false, "use line comment text as printed text when present")
}

func main() {
	flag.Parse()
	if err := doMain(); err != nil {
		panic(err)
	}
}

func doMain() error {
	if typeName == "" {
		return errors.New("must provide --type")
	}
	return tinyStringer{
		enumValuesSliceName:  enumValuesSliceName,
		files:                flag.Args(),
		lineComment:          lineComment,
		output:               output,
		stringToValueMapName: stringToValueMapName,
		typeName:             typeName,
		trimPrefix:           trimPrefix,
	}.stringify()
}

func (s tinyStringer) stringify() error {
	if len(s.files) == 0 {
		return errors.New("must provide at least one file argument")
	}
	// Make sure all input files are in the same package.
	var srcDir, whichFile string
	for _, file := range s.files {
		dir := filepath.Dir(file)
		if srcDir == "" {
			srcDir = dir
			whichFile = file
		} else {
			if srcDir != dir {
				return fmt.Errorf("all input files must be in the same source directory; got input file %s in directory %s, but input file %s in directory %s", whichFile, srcDir, file, dir)
			}
		}
	}
	if s.output == "" {
		s.output = filepath.Join(srcDir, strings.ToLower(s.typeName)+"_string.go")
	}

	parsedFiles, pkgName, err := parseAllFiles(s.files)
	if err != nil {
		return err
	}
	if err := validateType(parsedFiles, s.typeName); err != nil {
		return err
	}

	inOrder, nameToInt, nameToPrinted, err := s.computeConstantValues(parsedFiles)
	if err != nil {
		return err
	}

	if len(nameToInt) == 0 || len(nameToPrinted) == 0 {
		return fmt.Errorf("did not find enough constant values for type %s", s.typeName)
	}

	// Produce s.output.
	outputFile, err := os.Create(s.output)
	if err != nil {
		return err
	}
	defer func() {
		_ = outputFile.Close()
	}()
	fmt.Fprintf(outputFile, `// Code generated by "stringer"; DO NOT EDIT.

package %s

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
`, pkgName)
	for _, constName := range inOrder {
		if constName == "_" {
			continue
		}
		minus := "-"
		if nameToInt[constName] < 0 {
			// Implement the behavior of gofmt, which wants no space
			// between the operands unless the number on the right
			// is negative (would probably trigger some parse error).
			minus = " - "
		}
		fmt.Fprintf(outputFile, "	_ = x[%s%s%d]\n", constName, minus, nameToInt[constName])
	}
	receiverVar := "i"
	if _, ok := nameToInt[receiverVar]; ok {
		receiverVar = "_i"
		if _, ok := nameToInt[receiverVar]; ok {
			return fmt.Errorf("don't know how to choose a receiver variable because %s is a constant name", receiverVar)
		}
	}
	fmt.Fprintf(outputFile, `}

func (%s %s) String() string {
	switch %s {
`, receiverVar, s.typeName, receiverVar)
	seen := make(map[int]struct{})
	for _, constName := range inOrder {
		if constName == "_" {
			continue
		}
		if _, ok := seen[nameToInt[constName]]; ok {
			continue
		}
		fmt.Fprintf(outputFile, `	case %s:
		return "%s"
`, constName, nameToPrinted[constName])
		seen[nameToInt[constName]] = struct{}{}
	}
	fmt.Fprintf(outputFile, `	default:
		return "%s(" + strconv.FormatInt(int64(i), 10) + ")"
	}
}
`, s.typeName)
	if s.stringToValueMapName != "" {
		fmt.Fprintf(outputFile, `
var %s = map[string]%s{
`, s.stringToValueMapName, s.typeName)
		// Figure out the length of the longest const name to see how
		// much we need to pad it out.
		var maxLen int
		for _, constName := range inOrder {
			if len(nameToPrinted[constName]) > maxLen {
				maxLen = len(nameToPrinted[constName])
			}
		}
		for _, constName := range inOrder {
			if constName == "_" {
				continue
			}
			padding := strings.Repeat(" ", 1+maxLen-len(nameToPrinted[constName]))
			fmt.Fprintf(outputFile, `	"%s":%s%d,
`, nameToPrinted[constName], padding, nameToInt[constName])
		}
		fmt.Fprintf(outputFile, `}
`)
	}
	if s.enumValuesSliceName != "" {
		seen := make(map[int]struct{})
		fmt.Fprintf(outputFile, `
var %s = []%s{
`, s.enumValuesSliceName, s.typeName)
		inLexicographicOrder := make([]string, len(inOrder))
		copy(inLexicographicOrder, inOrder)
		// Clear duplicates, select the first one in order.
		i := 0
		for i < len(inLexicographicOrder) {
			constName := inLexicographicOrder[i]
			if _, ok := seen[nameToInt[constName]]; ok {
				inLexicographicOrder = append(inLexicographicOrder[:i], inLexicographicOrder[i+1:]...)
			} else {
				i += 1
				seen[nameToInt[constName]] = struct{}{}
			}
		}
		slices.SortFunc(inLexicographicOrder, func(a, b string) int {
			return cmp.Compare(nameToPrinted[a], nameToPrinted[b])
		})
		seen = make(map[int]struct{})
		for _, constName := range inLexicographicOrder {
			if constName == "_" {
				continue
			}
			if _, ok := seen[nameToInt[constName]]; ok {
				continue
			}
			fmt.Fprintf(outputFile, `	%s,
`, constName)
			seen[nameToInt[constName]] = struct{}{}
		}
		fmt.Fprintf(outputFile, `}
`)
	}

	return nil
}

// parseAllFiles returns a list of all the files parsed, the name of the package, and an error if one occurred.
func parseAllFiles(files []string) ([]*ast.File, string, error) {
	// Parse all files.
	fset := token.NewFileSet()
	parsedFiles := make([]*ast.File, 0, len(files))
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			return nil, "", err
		}
		parsedFiles = append(parsedFiles, parsed)
	}
	// All files should have the same package declaration. This will help us
	// determine what package the generated file should be in.
	var pkgName, whichFile string
	for i, file := range parsedFiles {
		if pkgName == "" {
			pkgName = file.Name.Name
			whichFile = files[i]
		} else {
			if pkgName != file.Name.Name {
				return nil, "", fmt.Errorf("all input files must have the same package name; got input file %s w/ 'package %s', but input file %s w/ 'package %s'", whichFile, pkgName, files[i], file.Name.Name)
			}
		}
	}
	return parsedFiles, pkgName, nil
}

func validateType(files []*ast.File, typeName string) error {
	// Find the definition of the type. Should be an alias for some
	// integer type.
	for _, file := range files {
		for _, decl := range file.Decls {
			var genDecl *ast.GenDecl
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			if genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					// Should never happen.
					return fmt.Errorf("unexpected error occurred while processing %+v", spec)
				}
				if typeSpec.Name.Name != typeName {
					continue
				}
				// Ensure the type is an alias for a built-in integer type.
				ident, ok := typeSpec.Type.(*ast.Ident)
				if !ok {
					return fmt.Errorf("expected identifier for definition of type %s", typeName)
				}
				var found bool
				for _, intType := range allowedIntegerTypes {
					if ident.Name == intType {
						found = true
						break
					}

				}
				if !found {
					return fmt.Errorf("expected an integer type for definition of type %s; got %s", typeName, ident.Name)
				}
			}
		}
	}
	return nil
}

func (s tinyStringer) computeConstantValues(
	files []*ast.File,
) (inOrder []string, nameToInt map[string]int, nameToPrinted map[string]string, err error) {
	nameToInt = make(map[string]int)
	nameToPrinted = make(map[string]string)

	for _, file := range files {
		for _, decl := range file.Decls {
			var genDecl *ast.GenDecl
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			if genDecl.Tok != token.CONST {
				continue
			}
			var inferAppropriateType, inIota bool
			var iotaVal int
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					// Should never happen.
					err = fmt.Errorf("unexpected error occurred while processing %+v", spec)
					return
				}
				if valueSpec.Type == nil && !inferAppropriateType {
					continue
				}
				ident, ok := valueSpec.Type.(*ast.Ident)
				if (ok && ident.Name != s.typeName) || (!ok && !inferAppropriateType) {
					inferAppropriateType = false
					continue
				}
				inferAppropriateType = true
				if len(valueSpec.Names) != 1 {
					err = fmt.Errorf("expected one name for constant of type %s; found %+v", s.typeName, valueSpec.Names)
					return
				}
				constName := valueSpec.Names[0].Name
				inOrder = append(inOrder, constName)
				// Check the value to see what value we'll assign to the constant.
				if valueSpec.Values == nil {
					if inIota {
						nameToInt[constName] = iotaVal
						iotaVal += 1
					} else {
						nameToInt[constName] = 0
					}
				} else if len(valueSpec.Values) != 1 {
					err = fmt.Errorf("expected one value for constant %s; found %+v", constName, valueSpec.Values)
					return
				} else if lit, ok := valueSpec.Values[0].(*ast.BasicLit); ok {
					if lit.Kind == token.INT {
						var intVal int64
						intVal, err = strconv.ParseInt(lit.Value, 0, 0)
						if err != nil {
							return
						}
						nameToInt[constName] = int(intVal)
						inIota = false
					} else if lit.Kind == token.CHAR {
						if len(lit.Value) != 3 {
							err = fmt.Errorf("expected string of form 'X' for character: got %s", lit.Value)
							return
						}
						if lit.Value[0] != '\'' || lit.Value[2] != '\'' {
							err = fmt.Errorf("expected string of form 'X' for character: got %s", lit.Value)
							return
						}
						nameToInt[constName] = int(lit.Value[1])
						inIota = false
					} else {
						err = fmt.Errorf("expected integer value for constant %s; found %s", constName, lit.Value)
						return
					}
				} else if ident, ok := valueSpec.Values[0].(*ast.Ident); ok {
					if ident.Name == "iota" {
						inIota = true
						nameToInt[constName] = iotaVal
						iotaVal += 1
					} else if otherValue, ok := nameToInt[ident.Name]; ok {
						nameToInt[constName] = otherValue
						inIota = false
					}
				} else if binExpr, ok := valueSpec.Values[0].(*ast.BinaryExpr); ok {
					// Handle iota + N or iota - N.
					iotaIdent, ok := binExpr.X.(*ast.Ident)
					if !ok || iotaIdent.Name != "iota" {
						err = fmt.Errorf("expected 'iota' in binary expression %+v; found %+v", binExpr, binExpr.X)
						return
					}
					var otherNumParsed int64
					if otherNum, ok := binExpr.Y.(*ast.BasicLit); ok && otherNum.Kind == token.INT {
						otherNumParsed, err = strconv.ParseInt(otherNum.Value, 0, 0)
						if err != nil {
							return
						}
					} else if otherRef, ok := binExpr.Y.(*ast.Ident); ok {
						otherNum, ok := nameToInt[otherRef.Name]
						if !ok {
							err = fmt.Errorf("could not find value of %s", otherRef.Name)
							return
						}
						otherNumParsed = int64(otherNum)
					} else {
						err = fmt.Errorf("couldn't parse second argument of binary expression %+v; found %+v", binExpr, binExpr.Y)
						return
					}
					if binExpr.Op == token.ADD {
						iotaVal = iotaVal + int(otherNumParsed)
					} else if binExpr.Op == token.SUB {
						iotaVal = iotaVal - int(otherNumParsed)
					}
					inIota = true
					nameToInt[constName] = iotaVal
					iotaVal += 1
				} else {
					err = fmt.Errorf("don't know how to process %+v", valueSpec.Values[0])
					return
				}

				// Determine the printed name of the constant.
				printedName := constName
				if s.lineComment && valueSpec.Comment != nil {
					printedName = strings.TrimSpace(valueSpec.Comment.Text())
				}
				if s.trimPrefix != "" {
					printedName = strings.TrimPrefix(printedName, s.trimPrefix)
				}
				nameToPrinted[constName] = printedName
			}
		}
	}
	return
}
