package cmd

import (
	"fmt"
	"path"
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

type upgradeCmd struct {
	installCmd
	deregister bool
}

func newUpgradeCmd() *cobra.Command {
	upgrade := &upgradeCmd{}
	cmd := &cobra.Command{
		Use:   "upgrade [cluster] [service] [path containing czecs.json]",
		Short: "Upgrade an existing service in an ECS cluster",
		Long: `This command upgrades a service to a new version of a task definition.

The task must already exist.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(3),
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
			return upgrade.run(args, svc)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&upgrade.strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&upgrade.balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&upgrade.values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&upgrade.stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.BoolVar(&upgrade.rollback, "rollback", false, "rollback to previous version if deployment failed")
	f.BoolVar(&upgrade.deregister, "deregister", false, "remove old task definition on success (or remove new task definition on failure)")

	return cmd
}

func (u *upgradeCmd) run(args []string, svc ecsiface.ECSAPI) error {
	cluster := args[0]
	service := args[1]
	czecsPath := args[2]
	var balances map[string]interface{}
	balances, err := mergeValues(u.balanceFiles, u.values, u.stringValues)
	if err != nil {
		return err
	}
	values := map[string]interface{}{
		"Values": balances,
	}
	log.Debugf("Values used for template: %#v", values)

	registerTaskDefinitionInput, err := tasks.ParseTaskDefinition(path.Join(czecsPath, "czecs.json"), values, u.strict)
	if err != nil {
		return errors.Wrap(err, "cannot parse task definition")
	}
	log.Debugf("Task definition: %+v", registerTaskDefinitionInput)

	describeServicesOutput, err := svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  &cluster,
		Services: []*string{&service},
	})
	if err != nil {
		return errors.Wrap(err, "cannot describe services")
	}
	if len(describeServicesOutput.Failures) != 0 {
		for _, failure := range describeServicesOutput.Failures {
			if *failure.Reason == "MISSING" {
				return fmt.Errorf("Service %#v does not exist in cluster %#v. Use outside tool or czecs install to create service", service, cluster)
			}
		}
		return fmt.Errorf("Error retrieving information about existing service %#v: %#v", service, describeServicesOutput.Failures)
	}
	var oldTaskDefinition *string
	for _, existingService := range describeServicesOutput.Services {
		if *existingService.ServiceName == service || *existingService.ServiceArn == service {
			oldTaskDefinition = existingService.TaskDefinition
		}
	}
	if oldTaskDefinition == nil {
		return fmt.Errorf("Error retrieving information about existing service %#v: no error/failure during DescribeServices but service not found in response", service)
	}
	log.Infof("Existing task definition %#v", *oldTaskDefinition)

	registerTaskDefinitionOutput, err := svc.RegisterTaskDefinition(registerTaskDefinitionInput)
	if err != nil {
		return errors.Wrap(err, "cannot register task definition")
	}
	taskDefn := registerTaskDefinitionOutput.TaskDefinition
	log.Infof("Successfully registered task definition %#v", *taskDefn.TaskDefinitionArn)

	err = deployUpgrade(svc, cluster, service, *taskDefn.TaskDefinitionArn)
	if err == nil {
		log.Debugf("Deregistering old task definition %#v", *oldTaskDefinition)
		if u.deregister && oldTaskDefinition != nil {
			svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
				TaskDefinition: oldTaskDefinition,
			})
		}
	} else if u.rollback {
		log.Warnf("Rolling back service %#v to old task definition %#v", service, oldTaskDefinition)
		rollbackErr := deployUpgrade(svc, cluster, service, *oldTaskDefinition)
		if rollbackErr != nil {
			// TODO(mbarrien): Report original
			return errors.Wrap(rollbackErr, "cannot rollback")
		}
		log.Debugf("Deregistering new task definition %#v", *taskDefn.TaskDefinitionArn)
		svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
			TaskDefinition: taskDefn.TaskDefinitionArn,
		})
		return err
	}
	return nil
}

func deployUpgrade(svc ecsiface.ECSAPI, cluster string, service string, taskDefnArn string) error {
	// Intentionally using printf directly, since we want this to be on the same line as the
	// progress dots.
	if log.GetLevel() >= log.InfoLevel {
		fmt.Printf("Updating service %#v in cluster %#v to task definition %#v", service, cluster, taskDefnArn)
	}
	// Get the primary deployment's updated date, default to now if missing
	updatedAt := time.Now()
	updateServiceOutput, err := svc.UpdateService(&ecs.UpdateServiceInput{
		Cluster:        &cluster,
		Service:        &service,
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

	opts := []request.WaiterOption{getFailOnAbortContext(updatedAt)}
	if log.GetLevel() == log.InfoLevel {
		opts = append(opts, sleepProgressWithContext)
	} else if log.GetLevel() == log.DebugLevel {
		opts = append(opts, debugSleepProgressWithContext)
	}
	return svc.WaitUntilServicesStableWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{updateServiceOutput.Service.ServiceArn}},
		opts...)
}

func getFailOnAbortContext(createdAt time.Time) request.WaiterOption {
	// Instead of waiting until the end of the timeout period, we examine the events log, looking for
	// a message which tells us the upgrade failed. So we need to filter out events that happened before
	// createdAt, to avoid reacting to errors from previous upgrades
	return func(waiter *request.Waiter) {
		waiter.Acceptors = append(waiter.Acceptors, request.WaiterAcceptor{
			State:    request.FailureWaiterState,
			Matcher:  request.PathAnyWaiterMatch,
			Argument: fmt.Sprintf("length(services[?events[?contains(message, 'unable') && updatedAt > %d]]) == `0`", createdAt.Unix()),
			Expected: true,
		})
	}
}

func sleepProgressWithContext(waiter *request.Waiter) {
	// Print something to the screen to show the waiter is still waiting.
	// At the end of the wait loop, print a newline.
	waiter.SleepWithContext = func(context aws.Context, duration time.Duration) error {
		fmt.Printf(".")
		result := aws.SleepWithContext(context, duration)
		if result != nil {
			fmt.Printf("\n")
		}
		return result
	}
}

func debugSleepProgressWithContext(waiter *request.Waiter) {
	var req *request.Request
	oldNewRequest := waiter.NewRequest
	waiter.NewRequest = func(opts []request.Option) (*request.Request, error) {
		newReq, err := oldNewRequest(opts)
		req = newReq
		return newReq, err
	}
	waiter.SleepWithContext = func(context aws.Context, duration time.Duration) error {
		log.Debugf("Sleeping, previous response: %+#v", req.Data)
		return aws.SleepWithContext(context, duration)
	}
}

func init() {
	rootCmd.AddCommand(newUpgradeCmd())
}
