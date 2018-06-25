package cmd

import (
	"fmt"
	"path"

	"github.com/chanzuckerberg/czecs/tasks"
	"github.com/imdario/mergo"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"k8s.io/helm/pkg/strvals"
)

var (
	balanceFiles []string
	values       []string
	stringValues []string
	strict       bool
	templates    []string
)

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Renders the czecs container definition template",
	Long: `Renders the czecs container definition template, substituting values
from any command line arguments or passed in via --balances. For example:

czecs template --set foo=bar --set baz=qux,spam=ham --balances balances.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		czecsPath := args[0]
		var balances map[string]interface{}
		balances, err := mergeValues(balanceFiles, values, stringValues)
		if err != nil {
			return err
		}
		values := map[string]interface{}{
			"Values": balances,
		}
		for _, templateName := range templates {
			taskDefn, err := tasks.ParseTaskDefinition(path.Join(czecsPath, templateName), values, strict)
			if err != nil {
				return err
			}
			fmt.Printf("%+v\n", taskDefn)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(templateCmd)

	f := templateCmd.Flags()
	f.BoolVar(&strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVarP(&templates, "execute", "x", []string{"czecs.json"}, "only execute the given templates")
}

func mergeValues(balanceFiles []string, values []string, stringValues []string) (map[string]interface{}, error) {
	base := map[string]interface{}{}
	for _, filePath := range balanceFiles {
		balances, err := tasks.ParseBalances(filePath)
		if err != nil {
			return nil, err
		}
		if err := mergo.Merge(&base, balances, mergo.WithOverride); err != nil {
			return nil, err
		}
	}
	for _, value := range values {
		if err := strvals.ParseInto(value, base); err != nil {
			return nil, errors.Wrap(err, "failed parsing --set data")
		}
	}
	for _, value := range stringValues {
		if err := strvals.ParseIntoString(value, base); err != nil {
			return nil, errors.Wrap(err, "failed parsing --set-string data")
		}
	}
	return base, nil
}
