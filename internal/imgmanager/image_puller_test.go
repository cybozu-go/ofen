package imgmanager

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

func TestImagePullStatusConcurrentAccess(t *testing.T) {
	t.Parallel()

	status := NewImagePullStatus()
	var wg sync.WaitGroup
	numGoroutines := 100

	// Test concurrent access to ImagePullStatus methods
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			// Test concurrent set and get operations
			status.SetImagePulling(id%2 == 0)
			_ = status.IsImagePulling()

			status.SetError(fmt.Errorf("error %d", id))
			_ = status.GetError()

			if id%3 == 0 {
				status.ClearError()
			}

			if id%4 == 0 {
				status.StartPulling()
				status.StopPulling()
			}

			_ = status.TryStartPulling()
		}(i)
	}

	wg.Wait()

	// Verify the status is in a consistent state
	assert.NotNil(t, status)
}

func TestImagePullStatusStateMachine(t *testing.T) {
	t.Parallel()

	status := NewImagePullStatus()

	// Initial state
	assert.False(t, status.IsImagePulling())
	assert.Nil(t, status.GetError())

	// Start pulling
	status.StartPulling()
	assert.True(t, status.IsImagePulling())
	assert.Nil(t, status.GetError())

	// Stop pulling
	status.StopPulling()
	assert.False(t, status.IsImagePulling())

	// Set error
	testErr := fmt.Errorf("test error")
	status.SetError(testErr)
	assert.Equal(t, testErr, status.GetError())

	// Clear error
	status.ClearError()
	assert.Nil(t, status.GetError())
}

func TestImagePullStatusTryStartPulling(t *testing.T) {
	t.Parallel()

	status := NewImagePullStatus()

	// First attempt should succeed
	assert.True(t, status.TryStartPulling())
	assert.True(t, status.IsImagePulling())
	assert.Nil(t, status.GetError())

	// Second attempt should fail while already pulling
	assert.False(t, status.TryStartPulling())
	assert.True(t, status.IsImagePulling())

	// Stop pulling and try again
	status.StopPulling()
	assert.True(t, status.TryStartPulling())
	assert.True(t, status.IsImagePulling())
}

func TestNodeImageSetStatusConcurrentAccess(t *testing.T) {
	t.Parallel()

	nodeStatus := &NodeImageSetStatus{
		Images: make(map[string]*ImagePullStatus),
		mutex:  sync.RWMutex{},
	}

	var wg sync.WaitGroup
	numGoroutines := 50

	// Test concurrent access to NodeImageSetStatus methods
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			imageName := fmt.Sprintf("test-image-%d", id%10)

			// Test concurrent GetOrCreateImageStatus
			status := nodeStatus.GetOrCreateImageStatus(imageName)
			assert.NotNil(t, status)

			// Test concurrent GetImageStatus
			retrievedStatus, exists := nodeStatus.GetImageStatus(imageName)
			assert.True(t, exists)
			assert.Equal(t, status, retrievedStatus)
		}(i)
	}

	wg.Wait()

	// Verify that we have the expected number of unique images
	assert.LessOrEqual(t, len(nodeStatus.Images), 10)
}

func TestImagePullerBasicOperations(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := &FakeContainerd{}
	puller := NewImagePuller(logger, fakeClient)

	nodeImageSetName := "test-nodeimageset"

	// Test NewNodeImageSetStatus
	puller.NewNodeImageSetStatus(nodeImageSetName)
	assert.True(t, puller.IsExistsNodeImageSetStatus(nodeImageSetName))
	assert.False(t, puller.IsExistsNodeImageSetStatus("nonexistent"))
}

func TestImagePullerIsImageExists(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	imageName := "test-image:latest"

	// Image doesn't exist initially
	exists := puller.IsImageExists(ctx, imageName)
	assert.False(t, exists)

	// Add image to fake client
	fakeClient.SetImageExists(imageName, true)
	exists = puller.IsImageExists(ctx, imageName)
	assert.True(t, exists)
}

func TestImagePullerPullImageExistingImage(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	nodeImageSetName := "test-nodeimageset"
	imageName := "test-image:latest"

	puller.NewNodeImageSetStatus(nodeImageSetName)

	// Set image as already existing
	fakeClient.SetImageExists(imageName, true)

	err := puller.PullImage(ctx, nodeImageSetName, imageName, ofenv1.RegistryPolicyDefault, nil)
	assert.NoError(t, err)
}

func TestImagePullerPullImageNonexistentNodeSet(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	imageName := "test-image:latest"

	// Try to pull for non-existent node set
	err := puller.PullImage(ctx, "nonexistent", imageName, ofenv1.RegistryPolicyDefault, nil)
	assert.NoError(t, err) // Should return nil without error
}

func TestImagePullerPullImageConcurrentPullingSameImage(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	nodeImageSetName := "test-nodeimageset"
	imageName := "test-image:latest"

	puller.NewNodeImageSetStatus(nodeImageSetName)

	// Manually set the image as pulling to test concurrent pull prevention
	nodeStatus := puller.status[nodeImageSetName]
	imageStatus := nodeStatus.GetOrCreateImageStatus(imageName)
	imageStatus.SetImagePulling(true)

	err := puller.PullImage(ctx, nodeImageSetName, imageName, ofenv1.RegistryPolicyDefault, nil)
	assert.NoError(t, err) // Should return nil (no-op) when already pulling
}

func TestImagePullerGetImageStatusStates(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	nodeImageSetName := "test-nodeimageset"
	imageName := "test-image:latest"

	puller.NewNodeImageSetStatus(nodeImageSetName)

	// Test WaitingForImageDownload state (initial state)
	status, message, err := puller.GetImageStatus(ctx, nodeImageSetName, imageName)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.WaitingForImageDownload, status)
	assert.Empty(t, message)

	// Test ImageDownloaded state
	fakeClient.SetImageExists(imageName, true)
	status, message, err = puller.GetImageStatus(ctx, nodeImageSetName, imageName)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloaded, status)
	assert.Empty(t, message)

	// Reset and test ImageDownloadInProgress state
	fakeClient.SetImageExists(imageName, false)
	nodeStatus := puller.status[nodeImageSetName]
	imageStatus := nodeStatus.GetOrCreateImageStatus(imageName)
	imageStatus.SetImagePulling(true)

	status, message, err = puller.GetImageStatus(ctx, nodeImageSetName, imageName)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloadInProgress, status)
	assert.Empty(t, message)

	// Test ImageDownloadFailed state
	imageStatus.SetImagePulling(false)
	testErr := fmt.Errorf("pull failed")
	imageStatus.SetError(testErr)

	status, message, err = puller.GetImageStatus(ctx, nodeImageSetName, imageName)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloadFailed, status)
	assert.Equal(t, "pull failed", message)
}

func TestImagePullerGetImageStatusNonexistentNodeImageSet(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	imageName := "test-image:latest"

	status, message, err := puller.GetImageStatus(ctx, "nonexistent", imageName)
	assert.NoError(t, err)
	assert.Empty(t, status)
	assert.Empty(t, message)
}

func TestImagePullerSubscribeDeleteEvent(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	nodeImageSetName := "test-nodeimageset"
	imageName := "test-image:latest"

	puller.NewNodeImageSetStatus(nodeImageSetName)
	nodeStatus := puller.status[nodeImageSetName]
	imageStatus := nodeStatus.GetOrCreateImageStatus(imageName)
	imageStatus.SetImagePulling(true)
	imageStatus.SetError(fmt.Errorf("some error"))

	// Subscribe to delete events
	deleteCh, err := puller.SubscribeDeleteEvent(ctx)
	require.NoError(t, err)

	// Send a delete event
	go func() {
		time.Sleep(50 * time.Millisecond)
		fakeClient.SendDeleteEvent(imageName)
	}()

	// Wait for the delete event
	select {
	case deletedImage := <-deleteCh:
		assert.Equal(t, imageName, deletedImage)

		// Verify that the image status was updated
		assert.False(t, imageStatus.IsImagePulling())
		assert.NoError(t, imageStatus.GetError())

	case <-ctx.Done():
		t.Fatal("Did not receive delete event in time")
	}
}

func TestImagePullerSubscribeDeleteEventMultipleNodeSets(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	imageName := "test-image:latest"

	// Create multiple node image sets with the same image
	nodeImageSets := []string{"nodeset1", "nodeset2", "nodeset3"}
	for _, nodeSet := range nodeImageSets {
		puller.NewNodeImageSetStatus(nodeSet)
		nodeStatus := puller.status[nodeSet]
		imageStatus := nodeStatus.GetOrCreateImageStatus(imageName)
		imageStatus.SetImagePulling(true)
		imageStatus.SetError(fmt.Errorf("some error"))
	}

	// Subscribe to delete events
	deleteCh, err := puller.SubscribeDeleteEvent(ctx)
	require.NoError(t, err)

	// Send a delete event
	go func() {
		time.Sleep(50 * time.Millisecond)
		fakeClient.SendDeleteEvent(imageName)
	}()

	// Wait for the delete event
	select {
	case deletedImage := <-deleteCh:
		assert.Equal(t, imageName, deletedImage)

		// Verify that all node image sets were updated
		for _, nodeSet := range nodeImageSets {
			nodeStatus := puller.status[nodeSet]
			imageStatus, exists := nodeStatus.GetImageStatus(imageName)
			assert.True(t, exists)
			assert.False(t, imageStatus.IsImagePulling())
			assert.NoError(t, imageStatus.GetError())
		}

	case <-ctx.Done():
		t.Fatal("Did not receive delete event in time")
	}
}

func TestImagePullerSubscribeDeleteEventContextCancellation(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx, cancel := context.WithCancel(context.Background())

	// Subscribe to delete events
	deleteCh, err := puller.SubscribeDeleteEvent(ctx)
	require.NoError(t, err)

	// Cancel context immediately
	cancel()

	// Channel should be closed
	select {
	case _, ok := <-deleteCh:
		assert.False(t, ok, "Channel should be closed")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Channel was not closed in time")
	}
}

func TestHandleDeleteEventValidEvent(t *testing.T) {
	t.Parallel()

	imageName := "test-image:latest"

	// Create a valid delete event
	deleteEvent := &eventtypes.ImageDelete{
		Name: imageName,
	}

	anyEvent, err := typeurl.MarshalAny(deleteEvent)
	require.NoError(t, err)

	envelope := &events.Envelope{
		Event: anyEvent,
	}

	result, err := handleDeleteEvent(envelope)
	assert.NoError(t, err)
	assert.Equal(t, imageName, result)
}

func TestHandleDeleteEventInvalidInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		envelope *events.Envelope
		wantErr  bool
	}{
		{
			name:     "nil envelope",
			envelope: nil,
			wantErr:  true,
		},
		{
			name: "nil event in envelope",
			envelope: &events.Envelope{
				Event: nil,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := handleDeleteEvent(tt.envelope)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, result)
			}
		})
	}
}

func TestHandleDeleteEventUnsupportedEventType(t *testing.T) {
	t.Parallel()

	// Create an unsupported event type (not ImageDelete)
	unsupportedEvent := &eventtypes.ImageCreate{
		Name: "test-image:latest",
	}

	anyEvent, err := typeurl.MarshalAny(unsupportedEvent)
	require.NoError(t, err)

	envelope := &events.Envelope{
		Event: anyEvent,
	}

	result, err := handleDeleteEvent(envelope)
	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "unsupported event type")
}
