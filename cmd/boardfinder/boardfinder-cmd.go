package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"strings"

	viamchess "viamchess"

	"go.viam.com/rdk/rimage"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <input.jpg> [output.jpg]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  If output is not specified, it will be <input>_output.jpg\n")
		os.Exit(1)
	}

	inputFile := os.Args[1]

	// Determine output file name
	var outputFile string
	if len(os.Args) >= 3 {
		outputFile = os.Args[2]
	} else {
		// Generate output filename: input.jpg -> input_output.jpg
		ext := filepath.Ext(inputFile)
		base := strings.TrimSuffix(inputFile, ext)
		outputFile = base + "_output" + ext
	}

	// Read input image
	input, err := rimage.ReadImageFromFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading image: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Image size: %dx%d\n", input.Bounds().Dx(), input.Bounds().Dy())

	// Find board corners
	corners, err := viamchess.FindBoard(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding board corners: %v\n", err)
		os.Exit(1)
	}

	if len(corners) != 4 {
		fmt.Fprintf(os.Stderr, "Expected 4 corners, found %d\n", len(corners))
		os.Exit(1)
	}

	fmt.Printf("Found corners:\n")
	fmt.Printf("  Top-left:     (%d, %d)\n", corners[0].X, corners[0].Y)
	fmt.Printf("  Top-right:    (%d, %d)\n", corners[1].X, corners[1].Y)
	fmt.Printf("  Bottom-right: (%d, %d)\n", corners[2].X, corners[2].Y)
	fmt.Printf("  Bottom-left:  (%d, %d)\n", corners[3].X, corners[3].Y)

	// Draw corners on output image
	output := image.NewRGBA(input.Bounds())
	draw.Draw(output, input.Bounds(), input, image.Point{}, draw.Src)

	// Mark detected corners with red circles and crosses
	red := color.RGBA{255, 0, 0, 255}
	for _, corner := range corners {
		drawCircle(output, corner.X, corner.Y, 10, red)
		drawCross(output, corner.X, corner.Y, 15, red)
	}

	// Save output image
	err = rimage.WriteImageToFile(outputFile, output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output image: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Saved output image to %s\n", outputFile)
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
