package viamchess

import (
	"testing"

	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/rimage"
	"go.viam.com/test"

	"github.com/erh/vmodutils/touch"
)

func TestPieceFinder1(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/hack1.jpg")
	test.That(t, err, test.ShouldBeNil)

	pc, err := pointcloud.NewFromFile("data/hack1.pcd", "")
	test.That(t, err, test.ShouldBeNil)

	squares, err := findBoardAndPieces(input, pc, touch.RealSenseProperties)
	test.That(t, err, test.ShouldBeNil)

	out, err := createDebugImage(input, squares)
	test.That(t, err, test.ShouldBeNil)

	err = rimage.WriteImageToFile("hack-test.jpg", out)
	test.That(t, err, test.ShouldBeNil)

}

func TestBoardPiece4(t *testing.T) {
	// Read the input image
	input, err := rimage.ReadImageFromFile("data/board4.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Read the pointcloud
	pc, err := pointcloud.NewFromFile("data/board4.pcd", "")
	test.That(t, err, test.ShouldBeNil)

	t.Run("find test", func(t *testing.T) {
		corners, err := findBoard(input)
		test.That(t, err, test.ShouldBeNil)
		test.That(t, len(corners), test.ShouldEqual, 4)
		t.Logf("Found corners: %v", corners)
	})

	squares, err := findBoardAndPieces(input, pc, touch.RealSenseProperties)
	test.That(t, err, test.ShouldBeNil)

	// Create debug image with square labels
	out, err := createDebugImage(input, squares)
	test.That(t, err, test.ShouldBeNil)

	// Save the output image for inspection
	err = rimage.WriteImageToFile("data/board4_piece_test_output.jpg", out)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved output image to data/board4_piece_test_output.jpg")

	// Verify we have 64 squares
	test.That(t, len(squares), test.ShouldEqual, 64)

	// Verify every square has a valid (non-empty) pointcloud
	emptySquares := []string{}
	for _, sq := range squares {
		if sq.pc == nil || sq.pc.Size() == 0 {
			emptySquares = append(emptySquares, sq.name)
		}
	}

	if len(emptySquares) > 0 {
		t.Errorf("Found %d squares with empty pointclouds: %v", len(emptySquares), emptySquares)
	}

	// Log square info for debugging
	for _, sq := range squares {
		pcSize := 0
		if sq.pc != nil {
			pcSize = sq.pc.Size()
		}
		t.Logf("Square %s: color=%d, pc_size=%d", sq.name, sq.color, pcSize)
	}
}
