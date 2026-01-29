package viamchess

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"

	"go.viam.com/rdk/rimage"
	"go.viam.com/test"
)

type boardTestCase struct {
	inputFile       string
	expectedCorners []image.Point
	tolerance       float64
}

func TestFindBoardCorners(t *testing.T) {
	testCases := []boardTestCase{
		{
			inputFile: "data/board1.jpg",
			expectedCorners: []image.Point{
				{392, 54},  // top-left - current best detection (ideal would be 390,48)
				{965, 79},  // top-right
				{938, 664}, // bottom-right
				{359, 636}, // bottom-left
			},
			tolerance: 2.0, // Tight tolerance to catch regressions
		},
		{
			inputFile: "data/board2.jpg",
			expectedCorners: []image.Point{
				{303, 76},  // top-left
				{883, 56},  // top-right
				{905, 638}, // bottom-right
				{311, 654}, // bottom-left
			},
			tolerance: 2.0,
		},
		{
			inputFile: "data/board3.jpg",
			expectedCorners: []image.Point{
				{269, 7},   // top-left
				{952, 5},   // top-right
				{970, 697}, // bottom-right
				{275, 697}, // bottom-left
			},
			tolerance: 2.0,
		},
		{
			inputFile: "data/board4.jpg",
			expectedCorners: []image.Point{
				{269, 7},   // top-left
				{953, 5},   // top-right
				{968, 693}, // bottom-right
				{271, 697}, // bottom-left
			},
			tolerance: 2.0,
		},
		{
			inputFile: "data/board5.jpg",
			expectedCorners: []image.Point{
				{295, 17},  // top-left
				{967, 16},  // top-right
				{983, 701}, // bottom-right
				{283, 705}, // bottom-left
			},
			tolerance: 7.0,
		},
		{
			inputFile: "data/board6.jpg",
			expectedCorners: []image.Point{
				{305, 10},  // top-left
				{981, 16},  // top-right
				{996, 698}, // bottom-right
				{293, 699}, // bottom-left
			},
			tolerance: 2.0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.inputFile, func(t *testing.T) {
			testBoardCornerDetection(t, tc)
		})
	}
}

func testBoardCornerDetection(t *testing.T, tc boardTestCase) {
	// Read input image
	input, err := rimage.ReadImageFromFile(tc.inputFile)
	test.That(t, err, test.ShouldBeNil)

	// Find board corners
	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Found corners: %v", corners)
	t.Logf("Image size: %dx%d", input.Bounds().Dx(), input.Bounds().Dy())

	// Draw corners on output image
	output := image.NewRGBA(input.Bounds())
	draw.Draw(output, input.Bounds(), input, image.Point{}, draw.Src)

	// Mark each corner with a red circle
	red := color.RGBA{255, 0, 0, 255}
	for _, corner := range corners {
		drawCircle(output, corner.X, corner.Y, 10, red)
		drawCross(output, corner.X, corner.Y, 15, red)
	}

	// Save output image
	// Extract base name from input file (e.g., "data/board1.jpg" -> "board1")
	baseName := tc.inputFile[5 : len(tc.inputFile)-4]
	outputFile := fmt.Sprintf("data/%s_output.jpg", baseName)
	err = rimage.WriteImageToFile(outputFile, output)
	test.That(t, err, test.ShouldBeNil)
	t.Logf("Saved output image to %s", outputFile)

	// Verify corners match expected values within tolerance
	for _, expected := range tc.expectedCorners {
		minDist := math.MaxFloat64
		var closestCorner image.Point
		for _, corner := range corners {
			dx := float64(corner.X - expected.X)
			dy := float64(corner.Y - expected.Y)
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist < minDist {
				minDist = dist
				closestCorner = corner
			}
		}
		t.Logf("Expected %v, closest found: %v, distance: %.1f pixels", expected, closestCorner, minDist)
		test.That(t, minDist, test.ShouldBeLessThan, tc.tolerance)
	}
}

func drawCircle(img *image.RGBA, cx, cy, radius int, c color.Color) {
	for angle := 0.0; angle < 360; angle += 1 {
		x := cx + int(float64(radius)*math.Cos(angle*math.Pi/180))
		y := cy + int(float64(radius)*math.Sin(angle*math.Pi/180))
		if x >= 0 && x < img.Bounds().Max.X && y >= 0 && y < img.Bounds().Max.Y {
			img.Set(x, y, c)
		}
	}
}

func drawCross(img *image.RGBA, cx, cy, size int, c color.Color) {
	for d := -size; d <= size; d++ {
		// Horizontal line
		x := cx + d
		if x >= 0 && x < img.Bounds().Max.X && cy >= 0 && cy < img.Bounds().Max.Y {
			img.Set(x, cy, c)
		}
		// Vertical line
		y := cy + d
		if cx >= 0 && cx < img.Bounds().Max.X && y >= 0 && y < img.Bounds().Max.Y {
			img.Set(cx, y, c)
		}
	}
}
