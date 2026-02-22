package imageutil

import (
	"image"
	"image/draw"
)

func Watermark(src image.Image, wmk image.Image, pt image.Point) image.Image {
	// 1. 保持画布与原图一致（包括可能的 Min 偏移）
	dst := image.NewNRGBA(src.Bounds())

	// 2. 绘制底层原图
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)

	// 3. 水印的宽高
	w, h := wmk.Bounds().Dx(), wmk.Bounds().Dy()

	// 4. 定义目标绘制区域
	// 注意：如果 src.Bounds().Min 不为 0，我们的 pt 必须基于这个 Min 进行偏移
	// 或者直接确保 pt 在计算时已经是绝对坐标
	dr := image.Rect(pt.X, pt.Y, pt.X+w, pt.Y+h)

	// 5. 绘制水印
	draw.Draw(dst, dr, wmk, wmk.Bounds().Min, draw.Over)

	//fmt.Printf("原图边界: %+v, 水印宽高: %d, %d, 计算的目标矩形: %+v\n", src.Bounds(), w, h, dr)
	return dst
}
