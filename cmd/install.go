package cmd

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"github.com/chanzuckerberg/czecs/tasks"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type installCmd struct {
	templateCmd
	rollback bool
	service  string
}

func newInstallCmd() *cobra.Command {
	inst := &installCmd{}
	cmd := &cobra.Command{
		Use:   "install [cluster] [task_definition.json]",
		Short: "Install a service into an ECS cluster",
		Long: `This command installs a service into an ECS cluster.

Limitations: No support for setting up load balancers through this command;
if you need load balancers; manually create an ECS service outside this tool
(e.g. using Terraform or aws command line tool), then use czecs upgrade.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(2),
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
			config := sess.Config

			svc := ecs.New(sess)
			return inst.run(args, svc, config)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&inst.strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&inst.balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&inst.values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&inst.stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.BoolVar(&inst.rollback, "rollback", false, "delete service if deployment failed")
	f.StringVarP(&inst.service, "name", "n", "", "service name; required for now")
	cmd.MarkFlagRequired("name")

	return cmd
}

func (i *installCmd) run(args []string, svc ecsiface.ECSAPI, config *aws.Config) error {
	cluster := args[0]
	taskDefnJSON := args[1]
	var balances map[string]interface{}
	balances, err := mergeValues(i.balanceFiles, i.values, i.stringValues)
	if err != nil {
		return err
	}
	values := map[string]interface{}{
		"Values": balances,
	}
	log.Debugf("Values used for template: %#v", values)

	registerTaskDefinitionInput, err := tasks.ParseTaskDefinition(taskDefnJSON, values, i.strict)
	if err != nil {
		return errors.Wrap(err, "cannot parse task definition")
	}
	log.Debugf("Task definition: %+v", registerTaskDefinitionInput)

	describeServicesOutput, err := svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  &cluster,
		Services: []*string{&i.service},
	})
	if err != nil {
		return errors.Wrap(err, "cannot describe services")
	}
	if len(describeServicesOutput.Failures) != 0 {
		return fmt.Errorf("Error retrieving information about existing service %#v: %#v", i.service, describeServicesOutput.Failures)
	}
	var oldTaskDefinition *string
	for _, existingService := range describeServicesOutput.Services {
		if *existingService.ServiceName == i.service || *existingService.ServiceArn == i.service {
			oldTaskDefinition = existingService.TaskDefinition
		}
	}
	if oldTaskDefinition != nil {
		return fmt.Errorf("Service %#v already exists in cluster %#v. Use czecs upgrade command to upgrade existing service", i.service, cluster)
	}

	registerTaskDefinitionOutput, err := svc.RegisterTaskDefinition(registerTaskDefinitionInput)
	if err != nil {
		return errors.Wrap(err, "cannot register task definition")
	}
	taskDefnArn := *registerTaskDefinitionOutput.TaskDefinition.TaskDefinitionArn
	log.Infof("Successfully registered task definition %#v", taskDefnArn)

	err = deployInstall(svc, cluster, i.service, taskDefnArn, config)
	if err != nil && i.rollback {
		log.Warnf("Rolling back service creation of %#v by deleting it", i.service)
		rollbackErr := rollbackInstall(svc, cluster, i.service)
		if rollbackErr != nil {
			return errors.Wrap(rollbackErr, "cannot rollback install")
		}
		log.Debugf("Deregistering new task definition %#v", taskDefnArn)
		_, rollbackErr = svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: &taskDefnArn,
		})
		if rollbackErr != nil {
			log.Warnf("Error deregistering task definition: %#v", rollbackErr.Error())
			log.Warnf("You will have to manually deregister the new task. Using AWS CLI you can run 'aws ecs deregister-task-definition --task-definition %s'", taskDefnArn)
			// Intentionally swallow error; this isn't fatal
		}
		return err
	}
	return nil
}

func deployInstall(svc ecsiface.ECSAPI, cluster string, service string, taskDefnArn string, config *aws.Config) error {
	// Intentionally using printf directly, since we want this to be on the same line as the
	// progress dots.
	if log.GetLevel() >= log.InfoLevel {
		fmt.Printf("Creating service %#v in cluster %#v with task definition %#v", service, cluster, taskDefnArn)
	}
	log.Infof("Service info location: https://%s.console.aws.amazon.com/ecs/home?region=%s#/clusters/%s/services/%s/details", *config.Region, *config.Region, cluster, service)

	// Get the primary deployment's updated date, default to now if missing
	createdAt := time.Now()
	createServiceOutput, err := svc.CreateService(&ecs.CreateServiceInput{
		Cluster:        &cluster,
		ServiceName:    &service,
		TaskDefinition: &taskDefnArn,
	})
	if err != nil {
		// TODO(mbarrien) Avoid rollback?
		return err
	}
	for _, deployment := range createServiceOutput.Service.Deployments {
		if *deployment.Status == "PRIMARY" {
			createdAt = *deployment.CreatedAt
			break
		}
	}

	opts := []request.WaiterOption{getFailOnAbortContext(createdAt)}
	if log.GetLevel() >= log.InfoLevel {
		opts = append(opts, sleepProgressWithContext)
	} else if log.GetLevel() == log.DebugLevel {
		opts = append(opts, debugSleepProgressWithContext)
	}
	return svc.WaitUntilServicesStableWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{createServiceOutput.Service.ServiceArn}},
		opts...)
}

func rollbackInstall(svc ecsiface.ECSAPI, cluster string, service string) error {
	// Get the primary deployment's updated date, default to now if missing
	deleteServiceOutput, err := svc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: &cluster,
		Service: &service,
	})
	if err != nil {
		return err
	}

	opts := []request.WaiterOption{}
	if log.GetLevel() == log.InfoLevel {
		opts = append(opts, sleepProgressWithContext)
	} else if log.GetLevel() == log.DebugLevel {
		opts = append(opts, debugSleepProgressWithContext)
	}
	return svc.WaitUntilServicesInactiveWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{deleteServiceOutput.Service.ServiceArn}},
		opts...)
}

func init() {
	rootCmd.AddCommand(newInstallCmd())
}
