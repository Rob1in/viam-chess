package viamchess

import (
	"image"
	"testing"

	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/rimage"
	"go.viam.com/test"

	"github.com/erh/vmodutils/touch"
)

func TestScale(t *testing.T) {
	test.That(t, scale(0, 10, .5), test.ShouldEqual, 5)
	test.That(t, scale(5, 15, .5), test.ShouldEqual, 10)

}

func TestComputeSquareBounds(t *testing.T) {

	corners := []image.Point{
		{0, 0},
		{80, 0},
		{80, 80},
		{0, 80},
	}

	res := computeSquareBounds(corners, 0, 0)
	test.That(t, res.Min.X, test.ShouldEqual, 0)
	test.That(t, res.Min.Y, test.ShouldEqual, 0)

	test.That(t, res.Max.X, test.ShouldEqual, 10)
	test.That(t, res.Max.Y, test.ShouldEqual, 10)

	corners = []image.Point{
		{360, 3},
		{940, 5},
		{1011, 688},
		{257, 680},
	}

	res = computeSquareBounds(corners, 0, 0)
	test.That(t, res.Min.X, test.ShouldEqual, 360)
	test.That(t, res.Min.Y, test.ShouldEqual, 3)

	res = computeSquareBounds(corners, 0, 6)
	test.That(t, res.Min.X, test.ShouldEqual, 283)
	test.That(t, res.Min.Y, test.ShouldEqual, 510)

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
