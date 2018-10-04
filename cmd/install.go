package cmd

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"github.com/chanzuckerberg/czecs/util"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

type installCmd struct {
	registerCmd
	rollback          bool
	service           string
	taskDefinitionArn string
	timeout           int
}

func newInstallCmd() *cobra.Command {
	inst := &installCmd{}
	cmd := &cobra.Command{
		Use:   "install [--task-definition-arn arn] [cluster] [task_definition.json]",
		Short: "Install a service into an ECS cluster",
		Long: `This command installs a service into an ECS cluster.

Limitations: No support for setting up load balancers through this command;
if you need load balancers; manually create an ECS service outside this tool
(e.g. using Terraform or aws command line tool), then use czecs upgrade.`,
		SilenceUsage: true,
		Args:         cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			logLevel := log.InfoLevel
			if debug { // debug overrides quiet
				logLevel = log.DebugLevel
			} else if quiet {
				logLevel = log.FatalLevel
			}
			log.SetLevel(logLevel)

			if (len(args) >= 2) == (inst.taskDefinitionArn != "") {
				return fmt.Errorf("exactly one of a task definition JSON filename (czecs.json) or a task definition ARN via --task-definition-arn must be provided")
			}

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
	f.StringVar(&inst.taskDefinitionArn, "task-definition-arn", "", "Use existing task definition instead of reading template file.")
	f.StringVarP(&inst.service, "name", "n", "", "service name; required for now")
	f.IntVarP(&inst.timeout, "timeout", "t", 600, "Seconds to wait for service to become stable before failing. Set to 0 for unlimited wait.")
	cmd.MarkFlagRequired("name")

	return cmd
}

func (i *installCmd) run(args []string, svc ecsiface.ECSAPI, config *aws.Config) error {
	cluster := args[0]

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

	var taskDefnArn string
	if len(args) >= 2 {
		taskDefnArn, err = i.registerTaskDefinition(args[1], svc)
		if err != nil {
			return err
		}
	} else {
		// Verify task definition exists
		_, err := svc.DescribeTaskDefinition(&ecs.DescribeTaskDefinitionInput{
			TaskDefinition: &i.taskDefinitionArn,
		})
		if err != nil {
			return errors.Wrapf(err, "cannot retrieve task definition %#v", i.taskDefinitionArn)
		}
		taskDefnArn = i.taskDefinitionArn
	}

	err = i.deployInstall(svc, cluster, taskDefnArn, config)
	if err != nil && i.rollback {
		log.Warnf("Rolling back service creation of %#v by deleting it", i.service)
		rollbackErr := i.rollbackInstall(svc, cluster)
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

func (i *installCmd) deployInstall(svc ecsiface.ECSAPI, cluster string, taskDefnArn string, config *aws.Config) error {
	log.Infof("Creating service %#v in cluster %#v with task definition %#v", i.service, cluster, taskDefnArn)
	log.Infof("Service info location: https://%s.console.aws.amazon.com/ecs/home?region=%s#/clusters/%s/services/%s/details", *config.Region, *config.Region, cluster, i.service)

	// Get the primary deployment's updated date, default to now if missing
	createdAt := time.Now()
	createServiceOutput, err := svc.CreateService(&ecs.CreateServiceInput{
		Cluster:        &cluster,
		ServiceName:    &i.service,
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

	// Intentionally using printf directly, since we want this to be on the same line as the
	// progress dots.
	if log.GetLevel() >= log.InfoLevel {
		fmt.Printf("Waiting for service %#v in cluster %#v with task definition %#v to be stable", i.service, cluster, taskDefnArn)
	}

	opts := append(util.WaiterDelay(i.timeout, 15), util.GetFailOnAbortContext(createdAt))
	if log.GetLevel() >= log.InfoLevel {
		opts = append(opts, util.SleepProgressWithContext)
	} else if log.GetLevel() == log.DebugLevel {
		opts = append(opts, util.DebugSleepProgressWithContext)
	}
	return svc.WaitUntilServicesStableWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{createServiceOutput.Service.ServiceArn}},
		opts...)
}

func (i *installCmd) rollbackInstall(svc ecsiface.ECSAPI, cluster string) error {
	// Get the primary deployment's updated date, default to now if missing
	deleteServiceOutput, err := svc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: &cluster,
		Service: &i.service,
	})
	if err != nil {
		return err
	}

	opts := util.WaiterDelay(i.timeout, 15)
	if log.GetLevel() == log.InfoLevel {
		opts = append(opts, util.SleepProgressWithContext)
	} else if log.GetLevel() == log.DebugLevel {
		opts = append(opts, util.DebugSleepProgressWithContext)
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
