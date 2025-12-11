package viamchess

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"github.com/golang/geo/r3"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/data"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/spatialmath"

	"github.com/erh/vmodutils/touch"
)

var BoardCameraHackModel = family.WithModel("board-camera-hack")

const minPieceSize = 20.0

func init() {
	resource.RegisterComponent(camera.API, BoardCameraHackModel,
		resource.Registration[camera.Camera, *BoardCameraHackConfig]{
			Constructor: newBoardHackCamera,
		},
	)
}

type BoardCameraHackConfig struct {
	Input string // this is the cropped camera for the board, TODO: what orientation???
}

func (cfg *BoardCameraHackConfig) Validate(path string) ([]string, []string, error) {
	if cfg.Input == "" {
		return nil, nil, fmt.Errorf("need an input")
	}
	return []string{cfg.Input}, nil, nil
}

func newBoardHackCamera(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (camera.Camera, error) {
	conf, err := resource.NativeConfig[*BoardCameraHackConfig](rawConf)
	if err != nil {
		return nil, err
	}

	return NewBoardCameraHack(ctx, deps, rawConf.ResourceName(), conf, logger)
}

func NewBoardCameraHack(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *BoardCameraHackConfig, logger logging.Logger) (camera.Camera, error) {
	var err error

	bc := &BoardCameraHack{
		name:   name,
		conf:   conf,
		logger: logger,
	}

	bc.input, err = camera.FromProvider(deps, conf.Input)
	if err != nil {
		return nil, err
	}

	bc.props, err = bc.Properties(ctx)
	if err != nil {
		return nil, err
	}

	return bc, nil
}

type BoardCameraHack struct {
	resource.AlwaysRebuild
	resource.TriviallyCloseable

	name   resource.Name
	conf   *BoardCameraHackConfig
	logger logging.Logger

	input camera.Camera
	props camera.Properties
}

func (bc *BoardCameraHack) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	return camera.GetImageFromGetImages(ctx, nil, bc, extra, nil)
}

func (bc *BoardCameraHack) Images(ctx context.Context, filterSourceNames []string, extra map[string]interface{}) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	ni, rm, err := bc.input.Images(ctx, nil, extra)
	if err != nil {
		return nil, rm, err
	}

	pc, err := bc.input.NextPointCloud(ctx, extra)
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

	dst, err := BoardDebugImageHack(srcImg, pc, bc.props)
	if err != nil {
		return nil, rm, err
	}

	result, err := camera.NamedImageFromImage(dst, ni[0].SourceName, "", data.Annotations{})
	if err != nil {
		return nil, rm, err
	}
	return []camera.NamedImage{result}, rm, nil
}

func BoardDebugImageHack(srcImg image.Image, pc pointcloud.PointCloud, props camera.Properties) (image.Image, error) {
	dst := image.NewRGBA(image.Rect(0, 0, srcImg.Bounds().Max.Y, srcImg.Bounds().Max.Y))

	xOffset := (srcImg.Bounds().Max.X - srcImg.Bounds().Max.Y) / 2

	squareSize := srcImg.Bounds().Max.Y / 8

	fmt.Printf("eliot %v -> %v squareSize: %d xOffset: %d\n", srcImg.Bounds(), dst.Bounds(), squareSize, xOffset)
	fmt.Printf("md: %v %v\n", pc.MetaData().MinZ, pc.MetaData().MaxZ)

	for rank := 1; rank <= 8; rank++ {
		for file := 'a'; file <= 'h'; file++ {
			xStartOffset := int((file - 'a')) * squareSize
			yStartOffset := (rank - 1) * squareSize

			srcRect := image.Rect(
				xStartOffset+xOffset,
				yStartOffset,
				xStartOffset+xOffset+squareSize,
				yStartOffset+squareSize,
			)

			dstRect := image.Rect(
				xStartOffset,
				yStartOffset,
				xStartOffset+squareSize,
				yStartOffset+squareSize,
			)

			subPc, err := touch.PCLimitToImageBoxes(pc, []*image.Rectangle{&srcRect}, nil, props)
			if err != nil {
				return nil, err
			}

			name := fmt.Sprintf("%s%d", string([]byte{byte(file)}), rank)

			pieceColor := estimatePieceColor(subPc)
			colorNames := []string{"", "W", "B"}
			meta := colorNames[pieceColor]

			fmt.Printf("%s : color: %v (%s)\n", name, pieceColor, meta)

			draw.Draw(dst, dstRect, srcImg, srcRect.Min, draw.Src)

			// put name in the middle of that square
			textX := dstRect.Min.X + squareSize/2 - len(name)*3
			textY := dstRect.Min.Y + squareSize/2 + 3
			drawString(dst, textX, textY, name+"-"+meta, color.RGBA{255, 0, 0, 255})
		}
	}

	return dst, nil
}

// 0 - blank, 1 - white, 2 - black
func estimatePieceColor(pc pointcloud.PointCloud) int {
	minZ := pc.MetaData().MaxZ - minPieceSize
	var totalR, totalG, totalB float64
	count := 0

	pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		if p.Z < minZ && d != nil && d.HasColor() {
			r, g, b := d.RGB255()
			totalR += float64(r)
			totalG += float64(g)
			totalB += float64(b)
			count++
		}
		return true
	})

	if count <= 10 {
		return 0 // blank - no piece detected
	}

	// calculate average brightness
	avgR := totalR / float64(count)
	avgG := totalG / float64(count)
	avgB := totalB / float64(count)
	brightness := (avgR + avgG + avgB) / 3.0

	// threshold to distinguish white vs black pieces
	if brightness > 128 {
		return 1 // white
	}
	return 2 // black
}

func drawString(dst *image.RGBA, x, y int, s string, c color.Color) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)},
	}
	d.DrawString(s)
}

func (bc *BoardCameraHack) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("DoCommand not supported")
}

func (bc *BoardCameraHack) NextPointCloud(ctx context.Context, extra map[string]interface{}) (pointcloud.PointCloud, error) {
	return nil, fmt.Errorf("NextPointCloud not supported")
}

func (bc *BoardCameraHack) Properties(ctx context.Context) (camera.Properties, error) {
	return camera.Properties{}, nil
}

func (bc *BoardCameraHack) Geometries(ctx context.Context, extra map[string]interface{}) ([]spatialmath.Geometry, error) {
	return nil, nil
}

func (bc *BoardCameraHack) Name() resource.Name {
	return bc.name
}
