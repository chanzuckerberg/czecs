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

type upgradeCmd struct {
	installCmd
	deregister bool
}

func newUpgradeCmd() *cobra.Command {
	upgrade := &upgradeCmd{}
	cmd := &cobra.Command{
		Use:   "upgrade [--task-definition-arn arn] [cluster] [service] [task_definition.json]",
		Short: "Upgrade an existing service in an ECS cluster",
		Long: `This command upgrades a service to a new version of a task definition.

The task must already exist.`,
		SilenceUsage: true,
		Args:         cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			logLevel := log.InfoLevel
			if debug { // debug overrides quiet
				logLevel = log.DebugLevel
			} else if quiet {
				logLevel = log.FatalLevel
			}
			log.SetLevel(logLevel)

			if (len(args) >= 3) == (upgrade.taskDefinitionArn != "") {
				return fmt.Errorf("exactly one of a task definition JSON filename (czecs.json) or a task definition ARN via --task-definition-arn must be provided")
			}

			sess := session.Must(session.NewSessionWithOptions(session.Options{
				SharedConfigState: session.SharedConfigEnable,
			}))
			config := sess.Config

			svc := ecs.New(sess)
			return upgrade.run(args, svc, config)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&upgrade.strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&upgrade.balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&upgrade.values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&upgrade.stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.BoolVar(&upgrade.rollback, "rollback", false, "rollback to previous version if deployment failed")
	f.BoolVar(&upgrade.deregister, "deregister", false, "remove old task definition on success (or remove new task definition on failure)")
	f.StringVar(&upgrade.taskDefinitionArn, "task-definition-arn", "", "Use existing task definition instead of reading template file.")
	f.IntVarP(&upgrade.timeout, "timeout", "t", 600, "Seconds to wait for service to become stable before failing. Set to 0 for unlimited wait.")

	return cmd
}

func (u *upgradeCmd) run(args []string, svc ecsiface.ECSAPI, config *aws.Config) error {
	cluster := args[0]
	u.service = args[1]

	describeServicesOutput, err := svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  &cluster,
		Services: []*string{&u.service},
	})
	if err != nil {
		return errors.Wrap(err, "cannot describe services")
	}
	if len(describeServicesOutput.Failures) != 0 {
		for _, failure := range describeServicesOutput.Failures {
			if *failure.Reason == "MISSING" {
				return fmt.Errorf("Service %#v does not exist in cluster %#v. Use outside tool or czecs install to create service", u.service, cluster)
			}
		}
		return fmt.Errorf("Error retrieving information about existing service %#v: %#v", u.service, describeServicesOutput.Failures)
	}
	var oldTaskDefinition *string
	for _, existingService := range describeServicesOutput.Services {
		if *existingService.ServiceName == u.service || *existingService.ServiceArn == u.service {
			oldTaskDefinition = existingService.TaskDefinition
		}
	}
	if oldTaskDefinition == nil {
		return fmt.Errorf("Error retrieving information about existing service %#v: no error/failure during DescribeServices but service not found in response", u.service)
	}
	log.Infof("Existing task definition %#v", *oldTaskDefinition)

	var taskDefnArn string
	if len(args) >= 3 {
		taskDefnArn, err = u.registerTaskDefinition(args[2], svc)
		if err != nil {
			return err
		}
	} else {
		// Verify task definition exists
		_, err := svc.DescribeTaskDefinition(&ecs.DescribeTaskDefinitionInput{
			TaskDefinition: &u.taskDefinitionArn,
		})
		if err != nil {
			return errors.Wrapf(err, "cannot retrieve task definition %#v", u.taskDefinitionArn)
		}
		taskDefnArn = u.taskDefinitionArn
	}

	err = u.deployUpgrade(svc, cluster, taskDefnArn, config)
	if err != nil {
		if u.rollback {
			log.Warnf("Rolling back service %#v to old task definition %#v", u.service, oldTaskDefinition)
			rollbackErr := u.deployUpgrade(svc, cluster, *oldTaskDefinition, config)
			if rollbackErr != nil {
				// TODO(mbarrien): Report original
				return errors.Wrap(rollbackErr, "cannot rollback")
			}
			log.Debugf("Deregistering new task definition %#v", taskDefnArn)
			_, deregisterErr := svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
				TaskDefinition: &taskDefnArn,
			})
			if deregisterErr != nil {
				log.Warnf("Error deregistering task definition after rollback: %#v", err.Error())
				log.Warnf("You will have to manually deregister the new task. Using AWS CLI you can run 'aws ecs deregister-task-definition --task-definition %s'", taskDefnArn)
				// Intentionally swallow error; let the original error bubble up
			}
		}
		return err
	}

	if u.deregister && oldTaskDefinition != nil {
		log.Debugf("Deregistering old task definition %#v", *oldTaskDefinition)
		_, err := svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: oldTaskDefinition,
		})
		if err != nil {
			log.Warnf("Error deregistering task definition: %#v", err.Error())
			log.Warnf("You will have to manually deregister the old task. Using AWS CLI you can run 'aws ecs deregister-task-definition --task-definition %s'", *oldTaskDefinition)
			// Intentionally swallow error; this isn't fatal
		}
	}
	return nil
}

func (u *upgradeCmd) deployUpgrade(svc ecsiface.ECSAPI, cluster string, taskDefnArn string, config *aws.Config) error {
	log.Infof("Updating service %#v in cluster %#v to task definition %#v", u.service, cluster, taskDefnArn)
	log.Infof("Service info location: https://%s.console.aws.amazon.com/ecs/home?region=%s#/clusters/%s/services/%s/details", *config.Region, *config.Region, cluster, u.service)

	// Get the primary deployment's updated date, default to now if missing
	updatedAt := time.Now()
	updateServiceOutput, err := svc.UpdateService(&ecs.UpdateServiceInput{
		Cluster:        &cluster,
		Service:        &u.service,
		TaskDefinition: &taskDefnArn,
	})
	if err != nil {
		// TODO(mbarrien) Avoid rollback?
		return err
	}
	for _, deployment := range updateServiceOutput.Service.Deployments {
		if *deployment.Status == "PRIMARY" {
			updatedAt = *deployment.UpdatedAt
			break
		}
	}

	// Intentionally using printf directly, since we want this to be on the same line as the
	// progress dots.
	if log.GetLevel() >= log.InfoLevel {
		fmt.Printf("Waiting for service %#v in cluster %#v to task definition %#v to be stable", u.service, cluster, taskDefnArn)
	}

	opts := append(util.WaiterDelay(u.timeout, 15), util.GetFailOnAbortContext(updatedAt))
	if log.GetLevel() >= log.InfoLevel {
		opts = append(opts, util.SleepProgressWithContext)
	} else if log.GetLevel() == log.DebugLevel {
		opts = append(opts, util.DebugSleepProgressWithContext)
	}

	return svc.WaitUntilServicesStableWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{updateServiceOutput.Service.ServiceArn}},
		opts...)
}

func init() {
	rootCmd.AddCommand(newUpgradeCmd())
}
