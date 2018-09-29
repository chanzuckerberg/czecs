package cmd

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"github.com/chanzuckerberg/czecs/tasks"
	"github.com/imdario/mergo"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/helm/pkg/strvals"
)

type registerCmd struct {
	balanceFiles []string
	values       []string
	stringValues []string
	strict       bool
	dryRun       bool
}

func newRegisterCmd() *cobra.Command {
	register := &registerCmd{}
	cmd := &cobra.Command{
		Use:   "register [task_definition.json]",
		Short: "Register a task definition with ECS",
		Long: `This command register a new version of a task definition.

It renders the czecs container definition template, substituting values
from any command line arguments or passed in via --balances. For example:

czecs register --set foo=bar --set baz=qux,spam=ham --balances balances.json czecs.json`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logLevel := log.InfoLevel
			if debug { // debug overrides quiet
				logLevel = log.DebugLevel
			} else if quiet {
				logLevel = log.FatalLevel
			}
			log.SetLevel(logLevel)

			sess := session.Must(session.NewSessionWithOptions(session.Options{
				SharedConfigState: session.SharedConfigEnable,
			}))
			svc := ecs.New(sess)
			return register.run(args, svc)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&register.strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&register.balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&register.values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&register.stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.BoolVar(&register.dryRun, "dry-run", false, "Do not actually register task definition; just print resulting task definition")
	return cmd
}

func (r *registerCmd) registerTaskDefinition(taskDefnJSON string, svc ecsiface.ECSAPI) (string, error) {
	var balances map[string]interface{}
	balances, err := mergeValues(r.balanceFiles, r.values, r.stringValues)
	if err != nil {
		return "", err
	}
	values := map[string]interface{}{
		"Values": balances,
	}
	log.Debugf("Values used for template: %#v", values)

	registerTaskDefinitionInput, err := tasks.ParseTaskDefinition(taskDefnJSON, values, r.strict)
	if err != nil {
		return "", errors.Wrap(err, "cannot parse task definition")
	}

	if r.dryRun {
		fmt.Printf("%#v\n", registerTaskDefinitionInput)
		return "", nil
	}

	log.Debugf("Task definition: %+v", registerTaskDefinitionInput)
	registerTaskDefinitionOutput, err := svc.RegisterTaskDefinition(registerTaskDefinitionInput)
	if err != nil {
		return "", errors.Wrap(err, "cannot register task definition")
	}
	taskDefn := registerTaskDefinitionOutput.TaskDefinition
	log.Infof("Successfully registered task definition %#v", *taskDefn.TaskDefinitionArn)
	return *taskDefn.TaskDefinitionArn, nil
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

func (r *registerCmd) run(args []string, svc ecsiface.ECSAPI) error {
	taskDefnArn, err := r.registerTaskDefinition(args[0], svc)
	if err != nil {
		return err
	}
	if !r.dryRun {
		fmt.Printf("%s\n", taskDefnArn)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(newRegisterCmd())
}
