package cmd

import (
	"fmt"
	"path"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/chanzuckerberg/czecs/tasks"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

var (
	deregister bool
)
var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade an existing service in an ECS cluster",
	Long: `This command upgrades a service to a new version of a task definition.

	The task must already exist.`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		cluster := args[0]
		service = args[1]
		czecsPath := args[2]
		var balances map[string]interface{}
		balances, err := mergeValues(balanceFiles, values, stringValues)
		if err != nil {
			return err
		}
		values := map[string]interface{}{
			"Values": balances,
		}
		registerTaskDefinitionInput, err := tasks.ParseTaskDefinition(path.Join(czecsPath, "czecs.json"), values, strict)
		if err != nil {
			return errors.Wrap(err, "cannot parse task definition")
		}

		if debug {
			fmt.Printf("%+v\n", registerTaskDefinitionInput)
		}
		sess := session.Must(session.NewSessionWithOptions(session.Options{
			SharedConfigState: session.SharedConfigEnable,
		}))
		svc := ecs.New(sess)
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
					return fmt.Errorf("Service %v does not exist in cluster %v. Use outside tool or czecs install to create service", service, cluster)
				}
			}
			return fmt.Errorf("Error retrieving information about existing service %v: %v", service, describeServicesOutput.Failures)
		}
		var oldTaskDefinition *string
		for _, existingService := range describeServicesOutput.Services {
			if *existingService.ServiceName == service || *existingService.ServiceArn == service {
				oldTaskDefinition = existingService.TaskDefinition
			}
		}
		if oldTaskDefinition == nil {
			return fmt.Errorf("Error retrieving information about existing service %v: no error/failure during DescribeServices but service not found in response", service)
		}
		registerTaskDefinitionOutput, err := svc.RegisterTaskDefinition(registerTaskDefinitionInput)
		if err != nil {
			return errors.Wrap(err, "cannot register task definition")
		}
		taskDefn := registerTaskDefinitionOutput.TaskDefinition
		err = deployUpgrade(svc, cluster, service, *taskDefn.TaskDefinitionArn)
		if err == nil {
			if deregister && oldTaskDefinition != nil {
				svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
					TaskDefinition: oldTaskDefinition,
				})
			}
		} else if rollback {
			err = deployUpgrade(svc, cluster, service, *oldTaskDefinition)
			if err != nil {
				return errors.Wrap(err, "cannot rollback")
			}
			svc.DeregisterTaskDefinition(&ecs.DeregisterTaskDefinitionInput{
				TaskDefinition: taskDefn.TaskDefinitionArn,
			})
		}
		return nil
	},
}

func deployUpgrade(svc *ecs.ECS, cluster string, service string, taskDefnArn string) error {
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
	if !quiet {
		opts = append(opts, sleepProgressWithContext)
	}
	return svc.WaitUntilServicesStableWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeServicesInput{
			Cluster:  &cluster,
			Services: []*string{updateServiceOutput.Service.ServiceArn}},
		opts...)
}

func getFailOnAbortContext(createdAt time.Time) request.WaiterOption {
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
	waiter.SleepWithContext = func(context aws.Context, duration time.Duration) error {
		fmt.Printf(".")
		result := aws.SleepWithContext(context, duration)
		if result != nil {
			fmt.Printf("\n")
		}
		return result
	}
}

func init() {
	rootCmd.AddCommand(upgradeCmd)

	f := upgradeCmd.Flags()
	f.BoolVar(&strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.BoolVarP(&quiet, "quiet", "q", false, "do not output to console; use return code to determine success/failure")
	f.BoolVar(&rollback, "rollback", false, "rollback to previous version if deployment failed")
	f.BoolVar(&deregister, "deregister", false, "remove old task definition on success (or remove new task definition on failure)")
}
