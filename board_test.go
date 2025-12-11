package viamchess

import (
	"testing"

	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/rimage"
	"go.viam.com/test"
)

func TestBoardDebugImage(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/hack-crop-2025-12-11_14_20_39.jpeg")
	test.That(t, err, test.ShouldBeNil)

	out, err := BoardDebugImage(input)
	test.That(t, err, test.ShouldBeNil)

	err = rimage.WriteImageToFile("test1.jpg", out)
	test.That(t, err, test.ShouldBeNil)

}

func TestBoardDebugImage2(t *testing.T) {
	input, err := pointcloud.NewFromFile("data/cropped1.pcd", "")
	test.That(t, err, test.ShouldBeNil)

	out, err := BoardDebugImage2(input)
	test.That(t, err, test.ShouldBeNil)

	err = rimage.WriteImageToFile("test2.jpg", out)
	test.That(t, err, test.ShouldBeNil)

}
