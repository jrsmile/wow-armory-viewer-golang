package model

import "math"

type Vec3 [3]float32
type Quat [4]float32
type Mat4 [16]float32

func Identity() Mat4 { return Mat4{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1} }
func Multiply(a, b Mat4) (c Mat4) {
	for col := range 4 {
		for row := range 4 {
			for k := range 4 {
				c[col*4+row] += a[k*4+row] * b[col*4+k]
			}
		}
	}
	return
}
func Translate(v Vec3) Mat4 { m := Identity(); m[12], m[13], m[14] = v[0], v[1], v[2]; return m }
func Scale(v Vec3) Mat4     { m := Identity(); m[0], m[5], m[10] = v[0], v[1], v[2]; return m }
func Rotation(q Quat) Mat4 {
	x, y, z, w := q[0], q[1], q[2], q[3]
	return Mat4{
		1 - 2*y*y - 2*z*z, 2*x*y + 2*z*w, 2*x*z - 2*y*w, 0,
		2*x*y - 2*z*w, 1 - 2*x*x - 2*z*z, 2*y*z + 2*x*w, 0,
		2*x*z + 2*y*w, 2*y*z - 2*x*w, 1 - 2*x*x - 2*y*y, 0,
		0, 0, 0, 1,
	}
}
func Perspective(aspect, near, far float32) Mat4 {
	f := float32(1 / math.Tan(math.Pi/8))
	return Mat4{f / aspect, 0, 0, 0, 0, f, 0, 0, 0, 0, (far + near) / (near - far), -1, 0, 0, 2 * far * near / (near - far), 0}
}
func normalize(q Quat) Quat {
	length := float32(math.Sqrt(float64(q[0]*q[0] + q[1]*q[1] + q[2]*q[2] + q[3]*q[3])))
	if length == 0 {
		return Quat{0, 0, 0, 1}
	}
	for i := range q {
		q[i] /= length
	}
	return q
}
func interpolateQuat(a, b Quat, t float32) Quat {
	dot := a[0]*b[0] + a[1]*b[1] + a[2]*b[2] + a[3]*b[3]
	if dot < 0 {
		for i := range b {
			b[i] = -b[i]
		}
	}
	var q Quat
	for i := range q {
		q[i] = a[i] + (b[i]-a[i])*t
	}
	return normalize(q)
}
