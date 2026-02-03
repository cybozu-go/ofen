package imgmanager

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ofenv1 "github.com/cybozu-go/ofen/api/v1"
)

const (
	testNodeImageSetName = "test-nodeimageset"
	testImageName        = "test-image:latest"
)

func TestImagePullStatusConcurrentAccess(t *testing.T) {
	t.Parallel()

	status := NewImagePullStatus()
	var wg sync.WaitGroup
	numGoroutines := 100

	// Test concurrent access to ImagePullStatus methods
	wg.Add(numGoroutines)
	for i := range numGoroutines {
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
		Images: sync.Map{},
	}

	var wg sync.WaitGroup
	numGoroutines := 50

	// Test concurrent access to NodeImageSetStatus methods
	wg.Add(numGoroutines)
	for i := range numGoroutines {
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

	// Verify that we have the expected number of unique images by counting
	imageCount := 0
	nodeStatus.Images.Range(func(key, value any) bool {
		imageCount++
		return true
	})
	assert.LessOrEqual(t, imageCount, 10)
}

func TestImagePullerBasicOperations(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := &FakeContainerd{}
	puller := NewImagePuller(logger, fakeClient)

	// Test NewNodeImageSetStatus
	puller.NewNodeImageSetStatus(testNodeImageSetName)
	assert.True(t, puller.IsExistsNodeImageSetStatus(testNodeImageSetName))
	assert.False(t, puller.IsExistsNodeImageSetStatus("nonexistent"))
}

func TestImagePullerDeleteNodeImageSetStatus(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := &FakeContainerd{}
	puller := NewImagePuller(logger, fakeClient)

	puller.NewNodeImageSetStatus(testNodeImageSetName)
	assert.True(t, puller.IsExistsNodeImageSetStatus(testNodeImageSetName))

	puller.DeleteNodeImageSetStatus(testNodeImageSetName)
	assert.False(t, puller.IsExistsNodeImageSetStatus(testNodeImageSetName))

	puller.DeleteNodeImageSetStatus("nonexistent")
	assert.False(t, puller.IsExistsNodeImageSetStatus("nonexistent"))
}

func TestImagePullerIsImageExists(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()

	// Image doesn't exist initially
	exists := puller.IsImageExists(ctx, testImageName)
	assert.False(t, exists)

	// Add image to fake client
	fakeClient.SetImageExists(testImageName, true)
	exists = puller.IsImageExists(ctx, testImageName)
	assert.True(t, exists)
}

func TestImagePullerPullImageExistingImage(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()

	puller.NewNodeImageSetStatus(testNodeImageSetName)

	// Set image as already existing
	fakeClient.SetImageExists(testImageName, true)

	err := puller.PullImage(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyDefault, nil)
	assert.NoError(t, err)
}

func TestImagePullerPullImageNonexistentNodeSet(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	// Try to pull for non-existent node set
	err := puller.PullImage(ctx, "nonexistent", testImageName, ofenv1.RegistryPolicyDefault, nil)
	assert.NoError(t, err) // Should return nil without error
}

func TestImagePullerPullImageConcurrentPullingSameImage(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	puller.NewNodeImageSetStatus(testNodeImageSetName)

	// Manually set the image as pulling to test concurrent pull prevention
	value, ok := puller.status.Load(testNodeImageSetName)
	require.True(t, ok)
	nodeStatus := value.(*NodeImageSetStatus)
	imageStatus := nodeStatus.GetOrCreateImageStatus(testImageName)
	imageStatus.SetImagePulling(true)

	err := puller.PullImage(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyDefault, nil)
	assert.NoError(t, err) // Should return nil (no-op) when already pulling
}

func TestImagePullerGetImageStatusStates(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()
	puller.NewNodeImageSetStatus(testNodeImageSetName)

	// Test WaitingForImageDownload state (initial state)
	status, message, err := puller.GetImageStatus(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyDefault)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.WaitingForImageDownload, status)
	assert.Empty(t, message)

	// Test ImageDownloaded state
	fakeClient.SetImageExists(testImageName, true)
	status, message, err = puller.GetImageStatus(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyDefault)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloaded, status)
	assert.Empty(t, message)

	// Reset and test ImageDownloadInProgress state
	fakeClient.SetImageExists(testImageName, false)
	value, ok := puller.status.Load(testNodeImageSetName)
	require.True(t, ok)
	nodeStatus := value.(*NodeImageSetStatus)
	imageStatus := nodeStatus.GetOrCreateImageStatus(testImageName)
	imageStatus.SetImagePulling(true)

	status, message, err = puller.GetImageStatus(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyDefault)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloadInProgress, status)
	assert.Empty(t, message)

	// Test ImageDownloadFailed state
	imageStatus.SetImagePulling(false)
	testErr := fmt.Errorf("pull failed")
	imageStatus.SetError(testErr)

	status, message, err = puller.GetImageStatus(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyDefault)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloadFailed, status)
	assert.Equal(t, "pull failed", message)

	// Test ImageDownloadFailed state with a temporary error
	imageStatus.ClearError()
	imageStatus.SetImagePulling(false)
	imageStatus.SetError(errdefs.ErrNotFound)
	status, message, err = puller.GetImageStatus(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyDefault)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloadFailed, status)
	assert.Equal(t, "not found", message)

	// Test ImageDownloadTemporarilyFailed state
	imageStatus.ClearError()
	imageStatus.SetImagePulling(false)
	imageStatus.SetError(errdefs.ErrNotFound)
	status, message, err = puller.GetImageStatus(ctx, testNodeImageSetName, testImageName, ofenv1.RegistryPolicyMirrorOnly)
	assert.NoError(t, err)
	assert.Equal(t, ofenv1.ImageDownloadTemporarilyFailed, status)
	assert.Equal(t, "not found", message)
}

func TestImagePullerGetImageStatusNonexistentNodeImageSet(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := NewFakeContainerd(nil)
	puller := NewImagePuller(logger, fakeClient)

	ctx := context.Background()

	status, message, err := puller.GetImageStatus(ctx, "nonexistent", testImageName, ofenv1.RegistryPolicyDefault)
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

	puller.NewNodeImageSetStatus(testNodeImageSetName)
	value, ok := puller.status.Load(testNodeImageSetName)
	require.True(t, ok)
	nodeStatus := value.(*NodeImageSetStatus)
	imageStatus := nodeStatus.GetOrCreateImageStatus(testImageName)
	imageStatus.SetImagePulling(true)
	imageStatus.SetError(fmt.Errorf("some error"))

	// Subscribe to delete events
	deleteCh, err := puller.SubscribeDeleteEvent(ctx)
	require.NoError(t, err)

	// Send a delete event
	go func() {
		time.Sleep(50 * time.Millisecond)
		err := fakeClient.SendDeleteEvent(testImageName)
		require.NoError(t, err)
	}()

	// Wait for the delete event
	select {
	case deletedImage := <-deleteCh:
		assert.Equal(t, testImageName, deletedImage)

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

	// Create multiple node image sets with the same image
	nodeImageSets := []string{"nodeset1", "nodeset2", "nodeset3"}
	for _, nodeSet := range nodeImageSets {
		puller.NewNodeImageSetStatus(nodeSet)
		value, ok := puller.status.Load(nodeSet)
		require.True(t, ok)
		nodeStatus := value.(*NodeImageSetStatus)
		imageStatus := nodeStatus.GetOrCreateImageStatus(testImageName)
		imageStatus.SetImagePulling(true)
		imageStatus.SetError(fmt.Errorf("some error"))
	}

	// Subscribe to delete events
	deleteCh, err := puller.SubscribeDeleteEvent(ctx)
	require.NoError(t, err)

	// Send a delete event
	go func() {
		time.Sleep(50 * time.Millisecond)
		err := fakeClient.SendDeleteEvent(testImageName)
		require.NoError(t, err)
	}()

	// Wait for the delete event
	select {
	case deletedImage := <-deleteCh:
		assert.Equal(t, testImageName, deletedImage)

		// Verify that all node image sets were updated
		for _, nodeSet := range nodeImageSets {
			value, ok := puller.status.Load(nodeSet)
			require.True(t, ok)
			nodeStatus := value.(*NodeImageSetStatus)
			imageStatus, exists := nodeStatus.GetImageStatus(testImageName)
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

	// Create a valid delete event
	deleteEvent := &eventtypes.ImageDelete{
		Name: testImageName,
	}

	anyEvent, err := typeurl.MarshalAny(deleteEvent)
	require.NoError(t, err)

	envelope := &events.Envelope{
		Event: anyEvent,
	}

	result, err := handleDeleteEvent(envelope)
	assert.NoError(t, err)
	assert.Equal(t, testImageName, result)
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

func TestImagePullerUpdateNodeImageSetStatus(t *testing.T) {
	t.Parallel()

	logger := logr.Discard()
	fakeClient := &FakeContainerd{}
	puller := NewImagePuller(logger, fakeClient)

	t.Run("nonexistent node image set", func(t *testing.T) {
		puller.UpdateNodeImageSetStatus("nonexistent", []string{"image1", "image2"})
	})

	t.Run("add new images", func(t *testing.T) {
		puller.NewNodeImageSetStatus(testNodeImageSetName)

		images := []string{"image1:latest", "image2:latest", "image3:latest"}
		puller.UpdateNodeImageSetStatus(testNodeImageSetName, images)

		value, ok := puller.status.Load(testNodeImageSetName)
		require.True(t, ok)

		nodeStatus := value.(*NodeImageSetStatus)
		for _, image := range images {
			_, exists := nodeStatus.GetImageStatus(image)
			assert.True(t, exists, "image %s should exist", image)
		}
	})

	t.Run("remove images not in new list", func(t *testing.T) {
		puller.NewNodeImageSetStatus(testNodeImageSetName + "-remove")

		nodeSetName := testNodeImageSetName + "-remove"
		initialImages := []string{"image1:latest", "image2:latest", "image3:latest"}
		puller.UpdateNodeImageSetStatus(nodeSetName, initialImages)

		newImages := []string{"image1:latest", "image4:latest"}
		puller.UpdateNodeImageSetStatus(nodeSetName, newImages)

		value, ok := puller.status.Load(nodeSetName)
		require.True(t, ok)

		nodeStatus := value.(*NodeImageSetStatus)

		_, exists := nodeStatus.GetImageStatus("image1:latest")
		assert.True(t, exists)

		_, exists = nodeStatus.GetImageStatus("image4:latest")
		assert.True(t, exists)

		_, exists = nodeStatus.GetImageStatus("image2:latest")
		assert.False(t, exists)

		_, exists = nodeStatus.GetImageStatus("image3:latest")
		assert.False(t, exists)
	})

	t.Run("preserve existing image status", func(t *testing.T) {
		puller.NewNodeImageSetStatus(testNodeImageSetName + "-preserve")

		nodeSetName := testNodeImageSetName + "-preserve"
		images := []string{"image1:latest"}
		puller.UpdateNodeImageSetStatus(nodeSetName, images)

		value, ok := puller.status.Load(nodeSetName)
		require.True(t, ok)

		nodeStatus := value.(*NodeImageSetStatus)
		imageStatus, exists := nodeStatus.GetImageStatus("image1:latest")
		require.True(t, exists)

		imageStatus.SetImagePulling(true)
		imageStatus.SetError(fmt.Errorf("test error"))

		puller.UpdateNodeImageSetStatus(nodeSetName, images)

		imageStatus, exists = nodeStatus.GetImageStatus("image1:latest")
		require.True(t, exists)
		assert.True(t, imageStatus.IsImagePulling())
		assert.NotNil(t, imageStatus.GetError())
	})

	t.Run("empty images list", func(t *testing.T) {
		puller.NewNodeImageSetStatus(testNodeImageSetName + "-empty")

		nodeSetName := testNodeImageSetName + "-empty"
		initialImages := []string{"image1:latest", "image2:latest"}
		puller.UpdateNodeImageSetStatus(nodeSetName, initialImages)

		puller.UpdateNodeImageSetStatus(nodeSetName, []string{})

		value, ok := puller.status.Load(nodeSetName)
		require.True(t, ok)

		nodeStatus := value.(*NodeImageSetStatus)

		_, exists := nodeStatus.GetImageStatus("image1:latest")
		assert.False(t, exists)

		_, exists = nodeStatus.GetImageStatus("image2:latest")
		assert.False(t, exists)
	})

	t.Run("concurrent updates", func(t *testing.T) {
		puller.NewNodeImageSetStatus(testNodeImageSetName + "-concurrent")

		nodeSetName := testNodeImageSetName + "-concurrent"
		var wg sync.WaitGroup
		numGoroutines := 10

		wg.Add(numGoroutines)
		for i := range numGoroutines {
			go func(id int) {
				defer wg.Done()
				images := []string{fmt.Sprintf("image%d:latest", id)}
				puller.UpdateNodeImageSetStatus(nodeSetName, images)
			}(i)
		}

		wg.Wait()

		value, ok := puller.status.Load(nodeSetName)
		require.True(t, ok)
		assert.NotNil(t, value)
	})
}
