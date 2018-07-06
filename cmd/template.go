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

type templateCmd struct {
	balanceFiles []string
	values       []string
	stringValues []string
	strict       bool
	templates    []string
}

func newTemplateCmd() *cobra.Command {
	template := &templateCmd{}
	var cmd = &cobra.Command{
		Use:   "template",
		Short: "Renders the czecs container definition template",
		Long: `Renders the czecs container definition template, substituting values
from any command line arguments or passed in via --balances. For example:

czecs template --set foo=bar --set baz=qux,spam=ham --balances balances.json`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return template.run(args)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&template.strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&template.balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&template.values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&template.stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVarP(&template.templates, "execute", "x", []string{"czecs.json"}, "only execute the given templates")

	return cmd
}

func (t *templateCmd) run(args []string) error {
	czecsPath := args[0]
	var balances map[string]interface{}
	balances, err := mergeValues(t.balanceFiles, t.values, t.stringValues)
	if err != nil {
		return err
	}
	values := map[string]interface{}{
		"Values": balances,
	}
	for _, templateName := range t.templates {
		taskDefn, err := tasks.ParseTaskDefinition(path.Join(czecsPath, templateName), values, t.strict)
		if err != nil {
			return err
		}
		fmt.Printf("%+v\n", taskDefn)
	}
	return nil
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

func init() {
	rootCmd.AddCommand(newTemplateCmd())
}
