package util

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/cloudflare/cfssl/log"
)

// GetFailOnAbortContext ends a ECS DescribeTask Waiter loop early if it finds messages in the event log that indicate the operation already failed.
func GetFailOnAbortContext(createdAt time.Time) request.WaiterOption {
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

// SleepProgressWithContext prints something to the screen to show the waiter is still waiting.
func SleepProgressWithContext(waiter *request.Waiter) {
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

// DebugSleepProgressWithContext prints extended debugging information to the screen while the waiter is still waiting.
func DebugSleepProgressWithContext(waiter *request.Waiter) {
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

// WaiterDelay returns the WaiterOptions to be able to delay a given amount of seconds,
// checking if the operation is done every defaultDelay seconds
func WaiterDelay(timeout int, defaultDelay int) []request.WaiterOption {
	if timeout == 0 {
		// The AWS code for the waiter looks for exact match on attempt count, and starts at 1. Setting 0
		// should make us loop indefinitely (or until the int overflow wraps around)
		return []request.WaiterOption{request.WithWaiterMaxAttempts(0)}
	}
	// Hardcode 6 seconds wait between each, like the default waiter.
	// "+ 1" because attempts is counted starting from 1
	maxAttempts := timeout/defaultDelay + 1
	lastDelay := timeout % defaultDelay
	if lastDelay == 0 {
		lastDelay = defaultDelay // When lastDelay evenly divides timeout, make the last one actually delay
	} else {
		maxAttempts++ // When we have remainder, make sure we have one last attempt to cover the remaining seconds
	}
	delayFunc := func(attempt int) time.Duration {
		// +1 here because of the 1-based counting
		if attempt+1 == maxAttempts {
			return time.Duration(lastDelay) * time.Second
		}
		return time.Duration(defaultDelay) * time.Second
	}
	return []request.WaiterOption{request.WithWaiterMaxAttempts(maxAttempts), request.WithWaiterDelay(delayFunc)}
}
