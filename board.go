package viamchess

import (
	"context"
	"fmt"
	"image"
	"image/color"

	"github.com/golang/geo/r3"

	"github.com/erh/vmodutils/touch"
	"github.com/lucasb-eyer/go-colorful"

	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/data"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/rimage"
	"go.viam.com/rdk/spatialmath"
)

var BoardCameraModel = family.WithModel("board-camera")

func init() {
	resource.RegisterComponent(camera.API, BoardCameraModel,
		resource.Registration[camera.Camera, *BoardCameraConfig]{
			Constructor: newBoardCamera,
		},
	)
}

type BoardCameraConfig struct {
	Input string // this is the cropped camera for the board, TODO: what orientation???
}

func (cfg *BoardCameraConfig) Validate(path string) ([]string, []string, error) {
	if cfg.Input == "" {
		return nil, nil, fmt.Errorf("need an input")
	}
	return []string{cfg.Input}, nil, nil
}

func newBoardCamera(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (camera.Camera, error) {
	conf, err := resource.NativeConfig[*BoardCameraConfig](rawConf)
	if err != nil {
		return nil, err
	}

	return NewBoardCamera(ctx, deps, rawConf.ResourceName(), conf, logger)
}

func NewBoardCamera(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *BoardCameraConfig, logger logging.Logger) (camera.Camera, error) {
	var err error

	bc := &BoardCamera{
		name:   name,
		conf:   conf,
		logger: logger,
	}

	bc.input, err = camera.FromProvider(deps, conf.Input)
	if err != nil {
		return nil, err
	}

	return bc, nil
}

type BoardCamera struct {
	resource.AlwaysRebuild
	resource.TriviallyCloseable

	name   resource.Name
	conf   *BoardCameraConfig
	logger logging.Logger

	input camera.Camera
}

func (bc *BoardCamera) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	return camera.GetImageFromGetImages(ctx, nil, bc, extra, nil)
}

func (bc *BoardCamera) Images(ctx context.Context, filterSourceNames []string, extra map[string]interface{}) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	ni, rm, err := bc.input.Images(ctx, nil, extra)
	if err != nil {
		return nil, rm, err
	}

	if len(ni) == 0 {
		return nil, rm, fmt.Errorf("no images returned from input camera")
	}

	srcImg, err := ni[0].Image(ctx)
	if err != nil {
		return nil, rm, err
	}

	dst, err := BoardDebugImage(srcImg)
	if err != nil {
		return nil, rm, err
	}

	result, err := camera.NamedImageFromImage(dst, ni[0].SourceName, "", data.Annotations{})
	if err != nil {
		return nil, rm, err
	}
	return []camera.NamedImage{result}, rm, nil

}

func BoardDebugImage(srcImg image.Image) (image.Image, error) {
	bounds := srcImg.Bounds()
	dst := image.NewRGBA(bounds)

	// fill in every pixel with just the hue of the pixel using colorful library and saturation and value of 1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			origColor := srcImg.At(x, y)
			cf, ok := colorful.MakeColor(origColor)
			if !ok {
				fmt.Printf("bad color: %v\n", origColor)
				continue
			}
			h, _, _ := cf.Hsv()
			newColor := colorful.Hsv(h, 1, 1)
			dst.Set(x, y, newColor)
		}
	}

	// draw grid lines splitting x and y into 64 squares (8x8)
	width := bounds.Dx()
	height := bounds.Dy()
	gridColor := color.RGBA{0, 0, 0, 255}

	// draw vertical lines
	for i := 0; i <= 8; i++ {
		x := bounds.Min.X + (width * i / 8)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			dst.Set(x, y, gridColor)
		}
	}

	// draw horizontal lines
	for i := 0; i <= 8; i++ {
		y := bounds.Min.Y + (height * i / 8)
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst.Set(x, y, gridColor)
		}
	}

	return dst, nil
}

func BoardDebugImage2(in pointcloud.PointCloud) (image.Image, error) {
	md := in.MetaData()

	xSize := md.MaxX - md.MinX
	ySize := md.MaxY - md.MinY

	fmt.Printf("xSize: %0.2f ySize: %0.2f\n", xSize, ySize)

	for col := 0.0; col < 8; col++ {
		for row := 0.0; row < 8; row++ {
			n := touch.PCCrop(
				in,
				r3.Vector{
					md.MinX + (row * (xSize / 8)),
					md.MinY + (col * (ySize / 8)),
					30,
				},
				r3.Vector{
					md.MinX + ((row + 1) * (xSize / 8)),
					md.MinY + ((col + 1) * (ySize / 8)),
					100,
				},
			)

			img := touch.PCToImage(n)
			err := rimage.WriteImageToFile(fmt.Sprintf("test-%d-%d.jpg", int(row), int(col)), img)
			if err != nil {
				panic(err)
			}
		}
	}
	panic(1)
}

func (bc *BoardCamera) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("DoCommand not supported")
}

func (bc *BoardCamera) NextPointCloud(ctx context.Context, extra map[string]interface{}) (pointcloud.PointCloud, error) {
	return nil, fmt.Errorf("NextPointCloud not supported")
}

func (bc *BoardCamera) Properties(ctx context.Context) (camera.Properties, error) {
	return camera.Properties{}, nil
}

func (bc *BoardCamera) Geometries(ctx context.Context, extra map[string]interface{}) ([]spatialmath.Geometry, error) {
	return nil, nil
}

func (bc *BoardCamera) Name() resource.Name {
	return bc.name
}
