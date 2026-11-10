package imgmanager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/workqueue"
)

func TestTaskDeduplication(t *testing.T) {
	t.Parallel()

	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Task]())
	defer queue.ShutDown()

	queue.Add(Task{NodeImageSetName: "a", Ref: "example.com/image:1"})
	queue.Add(Task{NodeImageSetName: "a", Ref: "example.com/image:1"})
	queue.Add(Task{NodeImageSetName: "b", Ref: "example.com/image:1"})
	require.Equal(t, 2, queue.Len())
}
