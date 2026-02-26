package viamchess

import (
	"image"
	"math"
	"sort"
)

// findBoard finds the four corners of the chess board.
// 1. Convert to grayscale
// 2. Detect edges with Sobel
// 3. Find lines with Hough transform
// 4. Merge nearby lines, remove isolated lines
// 5. Find border pair by fitting a regular 8-interval grid
// 6. Refine border lines using Theil-Sen estimator on edge pixels
// 7. Compute corners as line intersections
func findBoard(img image.Image) ([]image.Point, error) {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	gray := makeGrayImage(img)
	sobel := sobelEdgeDetection(gray, width, height)

	lines := houghLineDetection(sobel, width, height, 90)
	if len(lines) < 4 {
		return defaultCorners(width, height), nil
	}

	midX := width / 2
	midY := height / 2

	var hLines, vLines []lineWithPos
	for _, l := range lines {
		angleDeg := l.theta * 180 / math.Pi
		if angleDeg > 75 && angleDeg < 105 {
			y := (l.rho - float64(midX)*math.Cos(l.theta)) / math.Sin(l.theta)
			hLines = append(hLines, lineWithPos{l, y})
		} else if angleDeg < 15 || angleDeg > 165 {
			x := (l.rho - float64(midY)*math.Sin(l.theta)) / math.Cos(l.theta)
			vLines = append(vLines, lineWithPos{l, x})
		}
	}

	if len(hLines) < 2 || len(vLines) < 2 {
		return defaultCorners(width, height), nil
	}

	hLines = mergeByPosition(hLines, 15)
	vLines = mergeByPosition(vLines, 15)

	threshold := float64(height) * 0.20
	hLines = filterIsolatedLines(hLines, threshold)
	vLines = filterIsolatedLines(vLines, threshold)

	if len(hLines) < 2 || len(vLines) < 2 {
		return defaultCorners(width, height), nil
	}

	topLine, bottomLine := findBorderPairByGrid(hLines)
	leftLine, rightLine := findBorderPairByGrid(vLines)

	topLine = refineLineLocal(topLine, sobel, width, height, 80)
	bottomLine = refineLineLocal(bottomLine, sobel, width, height, 80)
	leftLine = refineLineLocal(leftLine, sobel, width, height, 80)
	rightLine = refineLineLocal(rightLine, sobel, width, height, 80)

	tl, ok1 := lineIntersection(topLine, leftLine)
	tr, ok2 := lineIntersection(topLine, rightLine)
	br, ok3 := lineIntersection(bottomLine, rightLine)
	bl, ok4 := lineIntersection(bottomLine, leftLine)

	if !ok1 || !ok2 || !ok3 || !ok4 {
		return defaultCorners(width, height), nil
	}

	return []image.Point{tl, tr, br, bl}, nil
}

// FindBoard is an exported version of findBoard for testing
func FindBoard(img image.Image) ([]image.Point, error) {
	return findBoard(img)
}

type refinePoint struct{ x, y float64 }

type lineWithPos struct {
	line Line
	pos  float64
}

// mergeByPosition merges lines within threshold distance, keeping highest-voted.
func mergeByPosition(lines []lineWithPos, threshold float64) []lineWithPos {
	var result []lineWithPos
	for _, l := range lines {
		tooClose := false
		for _, existing := range result {
			if math.Abs(l.pos-existing.pos) < threshold {
				tooClose = true
				break
			}
		}
		if !tooClose {
			result = append(result, l)
		}
	}
	return result
}

// filterIsolatedLines removes lines that have no neighbor within threshold distance.
func filterIsolatedLines(lines []lineWithPos, threshold float64) []lineWithPos {
	var result []lineWithPos
	for i, l := range lines {
		for j, other := range lines {
			if i != j && math.Abs(l.pos-other.pos) <= threshold {
				result = append(result, l)
				break
			}
		}
	}
	return result
}

// findBorderPairByGrid finds the pair of lines that best fits an 8-interval chess grid.
func findBorderPairByGrid(lines []lineWithPos) (Line, Line) {
	sort.Slice(lines, func(i, j int) bool { return lines[i].pos < lines[j].pos })

	if len(lines) <= 2 {
		return lines[0].line, lines[len(lines)-1].line
	}

	const intervals = 8

	bestI, bestJ := 0, len(lines)-1
	bestScore := 0

	var gridVotes [intervals + 1]int

	for i := range lines {
		for j := i + 1; j < len(lines); j++ {
			spacing := (lines[j].pos - lines[i].pos) / float64(intervals)
			if spacing < 10 {
				continue
			}

			for g := range gridVotes {
				gridVotes[g] = 0
			}

			for k := range lines {
				relPos := (lines[k].pos - lines[i].pos) / spacing
				nearest := math.Round(relPos)
				gridIdx := int(nearest)
				if gridIdx >= 0 && gridIdx <= intervals &&
					math.Abs(relPos-nearest) < 0.15 {
					if lines[k].line.votes > gridVotes[gridIdx] {
						gridVotes[gridIdx] = lines[k].line.votes
					}
				}
			}

			score := 0
			for _, v := range gridVotes {
				score += v
			}

			if score > bestScore {
				bestScore = score
				bestI, bestJ = i, j
			}
		}
	}

	return lines[bestI].line, lines[bestJ].line
}

func makeGrayImage(img image.Image) [][]int {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	gray := make([][]int, height)
	for y := range height {
		gray[y] = make([]int, width)
		for x := range width {
			c := img.At(bounds.Min.X+x, bounds.Min.Y+y)
			r, g, b, _ := c.RGBA()
			gray[y][x] = (int(r>>8) + int(g>>8) + int(b>>8)) / 3
		}
	}
	return gray
}

func defaultCorners(width, height int) []image.Point {
	return []image.Point{
		{width / 4, height / 8},
		{width * 3 / 4, height / 8},
		{width * 3 / 4, height * 7 / 8},
		{width / 4, height * 7 / 8},
	}
}

// Line represents a line in the form: rho = x*cos(theta) + y*sin(theta)
type Line struct {
	rho   float64
	theta float64
	votes int
}

type sobelResult struct {
	magnitude [][]int
	gx        [][]int
	gy        [][]int
}

func sobelEdgeDetection(gray [][]int, width, height int) sobelResult {
	mag := make([][]int, height)
	gxArr := make([][]int, height)
	gyArr := make([][]int, height)
	for y := range height {
		mag[y] = make([]int, width)
		gxArr[y] = make([]int, width)
		gyArr[y] = make([]int, width)
	}

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			gx := -gray[y-1][x-1] + gray[y-1][x+1] +
				-2*gray[y][x-1] + 2*gray[y][x+1] +
				-gray[y+1][x-1] + gray[y+1][x+1]

			gy := -gray[y-1][x-1] - 2*gray[y-1][x] - gray[y-1][x+1] +
				gray[y+1][x-1] + 2*gray[y+1][x] + gray[y+1][x+1]

			m := int(math.Sqrt(float64(gx*gx + gy*gy)))
			if m > 255 {
				m = 255
			}
			mag[y][x] = m
			gxArr[y][x] = gx
			gyArr[y][x] = gy
		}
	}

	return sobelResult{magnitude: mag, gx: gxArr, gy: gyArr}
}

// houghLineDetection detects lines using gradient-directed Hough transform.
func houghLineDetection(sobel sobelResult, width, height int, edgeThreshold int) []Line {
	edges := sobel.magnitude
	maxRho := int(math.Sqrt(float64(width*width + height*height)))
	numThetas := 720

	accumulator := make([][]int, 2*maxRho+1)
	for i := range accumulator {
		accumulator[i] = make([]int, numThetas)
	}

	cosTheta := make([]float64, numThetas)
	sinTheta := make([]float64, numThetas)
	for t := range numThetas {
		theta := float64(t) * math.Pi / float64(numThetas)
		cosTheta[t] = math.Cos(theta)
		sinTheta[t] = math.Sin(theta)
	}

	for y := range height {
		for x := range width {
			if edges[y][x] < edgeThreshold {
				continue
			}

			gx := float64(sobel.gx[y][x])
			gy := float64(sobel.gy[y][x])

			gradAngle := math.Atan2(gy, gx)
			if gradAngle < 0 {
				gradAngle += math.Pi
			}
			tCenter := int(gradAngle * float64(numThetas) / math.Pi)
			if tCenter >= numThetas {
				tCenter = 0
			}

			for dt := -5; dt <= 5; dt++ {
				t := (tCenter + dt + numThetas) % numThetas
				rho := float64(x)*cosTheta[t] + float64(y)*sinTheta[t]
				rhoIdx := int(rho) + maxRho
				if rhoIdx >= 0 && rhoIdx < 2*maxRho+1 {
					accumulator[rhoIdx][t]++
				}
			}
		}
	}

	var lines []Line
	voteThreshold := 100

	for rhoIdx := range 2*maxRho + 1 {
		for t := range numThetas {
			if accumulator[rhoIdx][t] < voteThreshold {
				continue
			}

			isMax := true
			for dr := -2; dr <= 2 && isMax; dr++ {
				for dt := -3; dt <= 3 && isMax; dt++ {
					if dr == 0 && dt == 0 {
						continue
					}
					nRho := rhoIdx + dr
					nT := (t + dt + numThetas) % numThetas
					if nRho >= 0 && nRho < 2*maxRho+1 {
						if accumulator[nRho][nT] > accumulator[rhoIdx][t] {
							isMax = false
						}
					}
				}
			}

			if isMax {
				rho := float64(rhoIdx - maxRho)
				theta := float64(t) * math.Pi / float64(numThetas)
				lines = append(lines, Line{rho: rho, theta: theta, votes: accumulator[rhoIdx][t]})
			}
		}
	}

	sort.Slice(lines, func(i, j int) bool {
		return lines[i].votes > lines[j].votes
	})

	return lines
}

func lineIntersection(l1, l2 Line) (image.Point, bool) {
	c1, s1 := math.Cos(l1.theta), math.Sin(l1.theta)
	c2, s2 := math.Cos(l2.theta), math.Sin(l2.theta)

	det := c1*s2 - c2*s1
	if math.Abs(det) < 1e-10 {
		return image.Point{}, false
	}

	x := (s2*l1.rho - s1*l2.rho) / det
	y := (c1*l2.rho - c2*l1.rho) / det

	return image.Point{X: int(math.Round(x)), Y: int(math.Round(y))}, true
}

// refineLineLocal refines a line using edge pixels within ±3 pixels, with Theil-Sen estimator.
func refineLineLocal(l Line, sobel sobelResult, width, height, edgeThreshold int) Line {
	edges := sobel.magnitude
	cosT, sinT := math.Cos(l.theta), math.Sin(l.theta)
	angleDeg := l.theta * 180 / math.Pi
	isHorizontal := angleDeg > 45 && angleDeg < 135

	var pts []refinePoint

	if isHorizontal {
		for x := range width {
			expectedY := (l.rho - float64(x)*cosT) / sinT
			yMin := int(math.Max(0, expectedY-3))
			yMax := int(math.Min(float64(height-1), expectedY+3))
			for y := yMin; y <= yMax; y++ {
				if edges[y][x] >= edgeThreshold {
					pts = append(pts, refinePoint{float64(x), float64(y)})
				}
			}
		}
	} else {
		for y := range height {
			expectedX := (l.rho - float64(y)*sinT) / cosT
			xMin := int(math.Max(0, expectedX-3))
			xMax := int(math.Min(float64(width-1), expectedX+3))
			for x := xMin; x <= xMax; x++ {
				if edges[y][x] >= edgeThreshold {
					pts = append(pts, refinePoint{float64(x), float64(y)})
				}
			}
		}
	}

	if len(pts) < 10 {
		return l
	}

	// Per-position median: one representative point per row/column
	medianPts := medianPerPosition(pts, isHorizontal)
	if len(medianPts) < 10 {
		return l
	}

	if isHorizontal {
		sort.Slice(medianPts, func(i, j int) bool { return medianPts[i].x < medianPts[j].x })
	} else {
		sort.Slice(medianPts, func(i, j int) bool { return medianPts[i].y < medianPts[j].y })
	}

	// Theil-Sen estimator: median of pairwise slopes
	n := len(medianPts)
	var slopes []float64
	minGap := n / 4
	if minGap < 10 {
		minGap = 10
	}
	step := 1
	if n > 200 {
		step = n / 100
	}
	for i := 0; i < n-minGap; i += step {
		for j := i + minGap; j < n; j += step {
			p1, p2 := medianPts[i], medianPts[j]
			if isHorizontal {
				dx := p2.x - p1.x
				if math.Abs(dx) > 1 {
					slopes = append(slopes, (p2.y-p1.y)/dx)
				}
			} else {
				dy := p2.y - p1.y
				if math.Abs(dy) > 1 {
					slopes = append(slopes, (p2.x-p1.x)/dy)
				}
			}
		}
	}

	if len(slopes) < 5 {
		if isHorizontal {
			a, b := fitLineHorizontal(medianPts)
			newTheta := math.Atan2(1.0, -a)
			if newTheta < 0 {
				newTheta += math.Pi
			}
			return Line{rho: b * math.Sin(newTheta), theta: newTheta, votes: l.votes}
		}
		a, b := fitLineVertical(medianPts)
		newTheta := math.Atan2(-a, 1.0)
		if newTheta < 0 {
			newTheta += math.Pi
		}
		return Line{rho: b * math.Cos(newTheta), theta: newTheta, votes: l.votes}
	}

	sort.Float64s(slopes)
	medianSlope := slopes[len(slopes)/2]

	var intercepts []float64
	for _, p := range medianPts {
		if isHorizontal {
			intercepts = append(intercepts, p.y-medianSlope*p.x)
		} else {
			intercepts = append(intercepts, p.x-medianSlope*p.y)
		}
	}
	sort.Float64s(intercepts)
	medianIntercept := intercepts[len(intercepts)/2]

	if isHorizontal {
		newTheta := math.Atan2(1.0, -medianSlope)
		if newTheta < 0 {
			newTheta += math.Pi
		}
		return Line{rho: medianIntercept * math.Sin(newTheta), theta: newTheta, votes: l.votes}
	}

	newTheta := math.Atan2(-medianSlope, 1.0)
	if newTheta < 0 {
		newTheta += math.Pi
	}
	return Line{rho: medianIntercept * math.Cos(newTheta), theta: newTheta, votes: l.votes}
}

// medianPerPosition groups edge points by position and returns one point per position
// using the median cross-line value.
func medianPerPosition(pts []refinePoint, isHorizontal bool) []refinePoint {
	groups := make(map[int][]float64)
	for _, p := range pts {
		if isHorizontal {
			groups[int(p.x)] = append(groups[int(p.x)], p.y)
		} else {
			groups[int(p.y)] = append(groups[int(p.y)], p.x)
		}
	}

	var result []refinePoint
	for pos, vals := range groups {
		sort.Float64s(vals)
		median := vals[len(vals)/2]
		if isHorizontal {
			result = append(result, refinePoint{float64(pos), median})
		} else {
			result = append(result, refinePoint{median, float64(pos)})
		}
	}
	return result
}

func fitLineHorizontal(pts []refinePoint) (a, b float64) {
	var sumX, sumY, sumXX, sumXY float64
	n := float64(len(pts))
	for _, p := range pts {
		sumX += p.x
		sumY += p.y
		sumXX += p.x * p.x
		sumXY += p.x * p.y
	}
	denom := n*sumXX - sumX*sumX
	if math.Abs(denom) < 1e-10 {
		return 0, sumY / n
	}
	a = (n*sumXY - sumX*sumY) / denom
	b = (sumY - a*sumX) / n
	return a, b
}

func fitLineVertical(pts []refinePoint) (a, b float64) {
	var sumX, sumY, sumYY, sumXY float64
	n := float64(len(pts))
	for _, p := range pts {
		sumX += p.x
		sumY += p.y
		sumYY += p.y * p.y
		sumXY += p.x * p.y
	}
	denom := n*sumYY - sumY*sumY
	if math.Abs(denom) < 1e-10 {
		return 0, sumX / n
	}
	a = (n*sumXY - sumY*sumX) / denom
	b = (sumX - a*sumY) / n
	return a, b
}
