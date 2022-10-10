package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ghodss/yaml"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-jsonnet"
	getter "github.com/hashicorp/go-getter"
	"github.com/pkg/errors"
)

const (
	autogenHeader = "## WARNING: This file was autogenerated!\n" +
		"## Manual modifications will be overwritten\n" +
		"## unless ignore: true is set in generate.yaml!\n" +
		"## Last Modification: %s.\n" +
		"\n"
)

type OverrideField struct {
	Path     string      `yaml:"path" json:"path"`
	Value    interface{} `yaml:"value,omitempty" json:"value,omitempty"`
	Override interface{} `yaml:"override,omitempty" json:"override,omitempty"`
	Ignore   bool        `yaml:"ignore" json:"ignore"`
}

type Composition struct {
	Name     string `yaml:"name" json:"name"`
	Provider string `yaml:"provider" json:"provider"`
	Default  bool   `yaml:"default" json:"default"`
}

type Generator struct {
	Group                string          `yaml:"group" json:"group"`
	Name                 string          `yaml:"name" json:"name"`
	Plural               *string         `yaml:"plural,omitempty" json:"plural,omitempty"`
	CRD                  string          `yaml:"crd" json:"crd"`
	Version              string          `yaml:"version" json:"version"`
	ScriptFileName       *string         `yaml:"scriptFile,omitempty"`
	ConnectionSecretKeys *[]string       `yaml:"connectionSecretKeys,omitempty" json:"connectionSecretKeys,omitempty"`
	Ignore               bool            `yaml:"ignore"`
	PatchExternalName    *bool           `yaml:"patchExternalName,omitempty" json:"patchExternalName,omitempty"`
	UIDFieldPath         *string         `yaml:"uidFieldPath,omitempty" json:"uidFieldPath,omitempty"`
	OverrideFields       []OverrideField `yaml:"overrideFields" json:"overrideFields"`
	Compositions         []Composition   `yaml:"compositions" json:"compositions"`
	crdSource            string
	configPath           string
}

type jsonnetOutput map[string]interface{}

func (g *Generator) LoadConfig(path string) *Generator {
	g.configPath = filepath.Dir(path)
	y, err := ioutil.ReadFile(path)
	if err != nil {
		log.Printf("Error loading generator: %+v", err)
	}
	err = yaml.Unmarshal(y, g)
	if err != nil {
		fmt.Printf("Error unmarshaling generator config: %v", err)
	}
	return g
}

func (g *Generator) LoadCRD(inputPath string) {
	crdTempDir, err := ioutil.TempDir("", "gencrd")
	if err != nil {
		fmt.Printf("Error creating CRD temp dir: %v", err)
	}

	defer os.RemoveAll(crdTempDir)

	crdFileName := filepath.Base(g.CRD)
	crdTempFile := filepath.Join(crdTempDir, crdFileName)

	if err != nil {
		fmt.Printf("Error creating CRD tempfile: %v", err)
	}

	client := &getter.Client{
		Ctx: context.Background(),
		Src: g.CRD,
		Pwd: inputPath,
		Dst: crdTempFile,
	}

	log.Printf("Retrieving CRD file from %s", g.CRD)
	err = client.Get()
	if err != nil {
		fmt.Printf("Get CRD: %v", err)
	}

	crd, err := ioutil.ReadFile(crdTempFile)
	if err != nil {
		fmt.Printf("Error reading from CRD tempfile: %v", err)
	}

	if len(crd) < 1 {
		fmt.Printf("CRD %s appears to be empty!", g.CRD)
	}

	r, err := yaml.YAMLToJSON(crd)
	if err != nil {
		fmt.Printf("Convert CRD to JSON: %v", err)
	}
	g.crdSource = string(r)
}

func (g *Generator) Exec(scriptPath, scriptFileOverride, outputPath string) {
	var fl string
	if scriptFileOverride != "" {
		fl = filepath.Join(scriptPath, scriptFileOverride)
	} else {
		fl = filepath.Join(scriptPath, "generate.jsonnet")
		if g.ScriptFileName != nil {
			fl = filepath.Join(scriptPath, *g.ScriptFileName)
		}
	}

	vm := jsonnet.MakeVM()

	j, err := json.Marshal(&g)
	if err != nil {
		fmt.Printf("Error creating jsonnet input: %s", err)
	}
	vm.ExtVar("config", string(j))
	vm.ExtVar("crd", g.crdSource)

	r, err := vm.EvaluateFile(fl)
	if err != nil {
		fmt.Printf("Error applying function %s: %s", fl, err)
	}

	jso := make(jsonnetOutput)

	err = json.Unmarshal([]byte(r), &jso)
	if err != nil {
		fmt.Printf("Error decoding jsonnet output: %s", err)
	}

	outPath := g.configPath
	if outputPath != "" {
		outPath = outputPath
	}

	header := []byte(fmt.Sprintf(autogenHeader,
		time.Now().Format("15:04:05 on 01-02-2006"),
	))

	for fn, fc := range jso {
		yo, err := yaml.Marshal(fc)
		if err != nil {
			fmt.Printf("Error converting %s to YAML: %v", fn, err)
		}
		fp := filepath.Join(outPath, fn) + ".yaml"

		// Check if file already exists
		if _, err := os.Stat(fp); err == nil {
			yi, err := ioutil.ReadFile(fp)
			if err != nil {
				fmt.Printf("Error reading from existing output file: %v", err)
			}
			ec := map[string]interface{}{}
			if err := yaml.Unmarshal(yi, &ec); err != nil {
				fmt.Printf("Error unmarshaling existing output file: %v", err)
			}

			if cmp.Equal(fc, ec) {
				continue
			}
		}

		fc := append(header, yo...)
		err = ioutil.WriteFile(fp, fc, 0644)
		if err != nil {
			fmt.Printf("Error writing Generated File %s: %v", fp, err)
		}
	}
}

func parseArgs(configFile, inputPath, scriptFile, scriptPath, outputPath *string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	_, b, _, ok := runtime.Caller(0)
	if !ok {
		return errors.New("Unable to get generator module path")
	}
	sp := filepath.Join(filepath.Dir(b), "functions")

	flag.StringVar(configFile, "inputName", "generate.yaml", "input filename to search for in current directory")
	flag.StringVar(inputPath, "inputPath", cwd, "input filename to search for in current directory")
	flag.StringVar(scriptFile, "scriptName", "", "script filename to execute against input file(s) (default: generate.jsonnet or specified in each input file)")
	flag.StringVar(scriptPath, "scriptPath", sp, "path where script files are loaded from ")
	flag.StringVar(outputPath, "outputPath", "", "path where output files are created (default: same directory as input file)")

	flag.Parse()

	return nil
}

func main() {
	var configFile, inputPath, scriptFile, scriptPath, outputPath string

	if err := parseArgs(&configFile, &inputPath, &scriptFile, &scriptPath, &outputPath); err != nil {
		fmt.Printf("Error parsing arguments: %s", err)
	}

	iGlob := filepath.Join(inputPath, "*/**/", configFile)
	ml, err := filepath.Glob(iGlob)
	if err != nil {
		fmt.Printf("Error finding generator files matching %s: %s", iGlob, err)
	}

	for _, m := range ml {
		g := (&Generator{
			OverrideFields: []OverrideField{},
			Compositions:   []Composition{},
		}).LoadConfig(m)
		if g.Ignore {
			fmt.Printf("Generator for %s asks to be ignored, skipping...", g.Name)
			continue
		}
		g.LoadCRD(inputPath)
		g.Exec(scriptPath, scriptFile, outputPath)
	}
}