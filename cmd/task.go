package cmd

import (
	"fmt"
	"strings"

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

type taskCmd struct {
	registerCmd
	cluster           string
	taskDefinitionArn string
}

func newTaskCmd() *cobra.Command {
	task := &taskCmd{}
	cmd := &cobra.Command{
		Use:   "task [--task-definition-arn arn] [--cluster cluster] [task.json]",
		Short: "Runs a one-time task to completion in an ECS cluster",
		Long: `This command runs a task on an ECS cluster

The task is considered successful if all containers in the task end with exit code 0.

It is based on a task definition JSON file appropriate for input to RunTask.
Multiple instances of the task may be run if the RunTask input contains a Count > 1.
If so, ALL tasks must`,
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
			return task.run(args, svc)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&task.strict, "strict", false, "fail on lint warnings")
	f.StringSliceVarP(&task.balanceFiles, "balances", "f", []string{}, "specify values in a JSON file or an S3 URL")
	f.StringSliceVar(&task.values, "set", []string{}, "set values on the command line (can repeat or use comma-separated values)")
	f.StringSliceVar(&task.stringValues, "set-string", []string{}, "set STRING values on the command line (can repeat or use comma-separated values)")
	f.StringVar(&task.cluster, "cluster", "", "Cluster to use, overriding any provided in the task JSON.")
	f.StringVar(&task.taskDefinitionArn, "task-definition-arn", "", "Task definition ARN to use, overriding any provided in the task JSON.")

	return cmd
}

func (t *taskCmd) parseTask(taskJSON string, svc ecsiface.ECSAPI) (*ecs.RunTaskInput, error) {
	var balances map[string]interface{}
	balances, err := mergeValues(t.balanceFiles, t.values, t.stringValues)
	if err != nil {
		return nil, err
	}
	values := map[string]interface{}{
		"Values": balances,
	}
	log.Debugf("Values used for template: %#v", values)

	runTaskInput, err := tasks.ParseTask(taskJSON, values, t.strict)
	if err != nil {
		return nil, errors.Wrap(err, "cannot parse task")
	}
	return runTaskInput, nil
}

func (t *taskCmd) run(args []string, svc ecsiface.ECSAPI) error {
	taskJSON := args[0]

	runTaskInput, err := t.parseTask(taskJSON, svc)
	if err != nil {
		return err
	}
	if t.cluster != "" {
		runTaskInput.Cluster = &t.cluster
	}
	if t.taskDefinitionArn != "" {
		runTaskInput.TaskDefinition = &t.taskDefinitionArn
	}

	describeTaskDefinitionOutput, err := svc.DescribeTaskDefinition(&ecs.DescribeTaskDefinitionInput{
		TaskDefinition: runTaskInput.TaskDefinition,
	})

	if err != nil {
		return errors.Wrapf(err, "error retrieving task definition ARN %#v; may not exist", t.taskDefinitionArn)
	}

	return runTask(svc, runTaskInput, describeTaskDefinitionOutput.TaskDefinition)
}

func runTask(svc ecsiface.ECSAPI, task *ecs.RunTaskInput, taskDefinition *ecs.TaskDefinition) error {
	log.Infof("Running task %#v", *task)
	runTaskOutput, err := svc.RunTask(task)
	if err != nil {
		return err
	}

	taskArns := make([]*string, len(runTaskOutput.Tasks))
	for i, task := range runTaskOutput.Tasks {
		taskArns[i] = task.TaskArn
	}
	log.Debugf("Run tasks output: Task ARNs: %#v, Failures %#v", taskArns, runTaskOutput.Failures)

	if log.GetLevel() >= log.InfoLevel {
		for _, taskArn := range taskArns {
			// Extract the task ID to derive the URL; have to parse it out of the ARN
			taskArnParts := strings.Split(*taskArn, ":")
			lastTaskArnPart := taskArnParts[len(taskArnParts)-1]
			slashSplit := strings.Split(lastTaskArnPart, "/")
			taskID := slashSplit[len(slashSplit)-1]

			// Go through all container definitions, for any with awslogs fully configured, log URL to find task information.
			// Since the run task can have multiple instances of the task, show all potential logs.
			for _, containerDefn := range taskDefinition.ContainerDefinitions {
				logConfiguration := containerDefn.LogConfiguration
				if *logConfiguration.LogDriver == "awslogs" {
					containerName := *containerDefn.Name
					// awslogs-stream-prefix is optional if on EC2 ECS, and would default to the instance ID it is scheduled on.
					// Since in such cases we would not be able to extract/predict the instance ID, we don't log the task log location
					// unless awslogs-stream-prefix is explicitly provided.
					if streamPrefix, ok := logConfiguration.Options["awslogs-stream-prefix"]; ok {
						// awslogs-group and awslogs-region are required container definition arguments; if we got here, we can assume
						// they are in the options without explicitly checking for existence.
						logGroup := logConfiguration.Options["awslogs-group"]
						region := logConfiguration.Options["awslogs-region"]
						log.Infof("Task log location: https://%s.console.aws.amazon.com/cloudwatch/home?region=%s#logEventViewer:group=%s;stream=%s/%s/%s", *region, *region, *logGroup, *streamPrefix, containerName, taskID)
					}
				}
			}
		}
	}

	// Intentionally do the failure check after logging the task locations of those that were sucessful
	if len(runTaskOutput.Failures) != 0 {
		return fmt.Errorf("failed to start all instances of task %s; failures %#v", *task.TaskDefinition, runTaskOutput.Failures)
	}

	opts := []request.WaiterOption{}
	if log.GetLevel() >= log.InfoLevel {
		opts = append(opts, sleepProgressWithContext)
	} else if log.GetLevel() == log.DebugLevel {
		opts = append(opts, debugSleepProgressWithContext)
	}

	// Intentionally using printf directly, since we want this to be on the same line as the
	// progress dots.
	if log.GetLevel() >= log.InfoLevel {
		taskArnStrings := make([]string, len(taskArns))
		for i, taskArn := range taskArns {
			taskArnStrings[i] = *taskArn
		}
		fmt.Printf("Waiting for tasks %v to finish", taskArnStrings)
	}

	// Note: Default is 10 minutes; is this enough?
	// If not can add WithWaiterMaxAttempts to opts above to adjust
	err = svc.WaitUntilTasksStoppedWithContext(
		aws.BackgroundContext(),
		&ecs.DescribeTasksInput{
			Cluster: task.Cluster,
			Tasks:   taskArns},
		opts...)
	if err != nil {
		return errors.Wrap(err, "error while waiting for task instances to complete")
	}

	// Check that all exit codes of all containers in all tasks had exit code zero.
	describeTasksOutput, err := svc.DescribeTasks(&ecs.DescribeTasksInput{
		Cluster: task.Cluster,
		Tasks:   taskArns,
	})
	if err != nil {
		return errors.Wrap(err, "unable to retrive task statuses during verification")
	}
	if len(describeTasksOutput.Failures) != 0 {
		return fmt.Errorf("failures occured while running tasks: %#v", describeTasksOutput.Failures)
	}
	if len(describeTasksOutput.Tasks) != len(taskArns) {
		// Somehow have no failures, but also missing tasks; really unexpected error
		return fmt.Errorf("tried to retrieve %d task ARN(s) %#v, but only retrieved info about %d tasks", len(taskArns), taskArns, len(describeTasksOutput.Tasks))
	}
	for _, task := range describeTasksOutput.Tasks {
		if *task.LastStatus != "STOPPED" {
			return fmt.Errorf("expected all tasks to be stopped, but task ARN %s was in state %#v", *task.TaskArn, task.LastStatus)
		}
		for _, container := range task.Containers {
			if container.ExitCode == nil {
				return fmt.Errorf("container %s in task %s has no exit code; task may have failed before container started", *container.Name, *task.TaskArn)
			}
			if *container.ExitCode != 0 {
				return fmt.Errorf("container %s in task %s exited with non-zero exit code %d; see logs for details", *container.Name, *task.TaskArn, *container.ExitCode)
			}
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(newTaskCmd())
}
