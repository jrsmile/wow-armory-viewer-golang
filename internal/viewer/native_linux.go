//go:build linux

package viewer

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/jrsmile/armory/internal/model"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Config describes what the viewer shows. Lookup runs off the GL thread and
// returns the scene to display for an Armory URL typed into the window.
type Config struct {
	Scene      *model.Scene
	Lookup     func(string) (*model.Scene, error)
	URL        string // looked up immediately when set
	Status     string
	Fullscreen bool
	SelfTest   bool
	Screenshot string
	// Progress, when set, is shown as a URL log and progress bar at the
	// bottom of the window while a lookup is running.
	Progress *Progress
	// Background, when set, is drawn behind the character, scaled to cover
	// the window while keeping its aspect ratio.
	Background *image.NRGBA
}

type sdl struct {
	init                func(uint32) int32
	quit                func()
	createWindow        func(string, int32, int32, int32, int32, uint32) uintptr
	destroyWindow       func(uintptr)
	glSetAttribute      func(int32, int32) int32
	glCreateContext     func(uintptr) uintptr
	glDeleteContext     func(uintptr)
	glGetProcAddress    func(string) uintptr
	glSwapWindow        func(uintptr)
	glSetSwapInterval   func(int32) int32
	glGetDrawableSize   func(uintptr, *int32, *int32)
	pollEvent           func(unsafe.Pointer) int32
	startTextInput      func()
	stopTextInput       func()
	getError            func() unsafe.Pointer
	getClipboardText    func() unsafe.Pointer
	free                func(unsafe.Pointer)
	setWindowFullscreen func(uintptr, uint32) int32
	showCursor          func(int32) int32
}

type gl struct {
	clearColor              func(float32, float32, float32, float32)
	clear                   func(uint32)
	enable                  func(uint32)
	disable                 func(uint32)
	viewport                func(int32, int32, int32, int32)
	createShader            func(uint32) uint32
	shaderSource            func(uint32, int32, unsafe.Pointer, unsafe.Pointer)
	compileShader           func(uint32)
	getShaderiv             func(uint32, uint32, *int32)
	getShaderInfoLog        func(uint32, int32, *int32, unsafe.Pointer)
	deleteShader            func(uint32)
	createProgram           func() uint32
	attachShader            func(uint32, uint32)
	linkProgram             func(uint32)
	getProgramiv            func(uint32, uint32, *int32)
	getProgramInfoLog       func(uint32, int32, *int32, unsafe.Pointer)
	useProgram              func(uint32)
	deleteProgram           func(uint32)
	getUniformLocation      func(uint32, string) int32
	uniformMatrix4fv        func(int32, int32, uint8, unsafe.Pointer)
	uniform1i               func(int32, int32)
	uniform1f               func(int32, float32)
	uniform2f               func(int32, float32, float32)
	genVertexArrays         func(int32, *uint32)
	bindVertexArray         func(uint32)
	deleteVertexArrays      func(int32, *uint32)
	genBuffers              func(int32, *uint32)
	bindBuffer              func(uint32, uint32)
	bufferData              func(uint32, int64, unsafe.Pointer, uint32)
	deleteBuffers           func(int32, *uint32)
	enableVertexAttribArray func(uint32)
	vertexAttribPointer     func(uint32, int32, uint32, uint8, int32, uintptr)
	drawElements            func(uint32, int32, uint32, uintptr)
	drawArrays              func(uint32, int32, int32)
	genTextures             func(int32, *uint32)
	activeTexture           func(uint32)
	bindTexture             func(uint32, uint32)
	texImage2D              func(uint32, int32, int32, int32, int32, int32, uint32, uint32, unsafe.Pointer)
	texParameteri           func(uint32, uint32, int32)
	deleteTextures          func(int32, *uint32)
	blendFunc               func(uint32, uint32)
	depthMask               func(uint8)
	getString               func(uint32) unsafe.Pointer
	getError                func() uint32
	readPixels              func(int32, int32, int32, int32, uint32, uint32, unsafe.Pointer)
}

func cString(ptr unsafe.Pointer) string {
	if ptr == nil {
		return ""
	}
	var out []byte
	for i := uintptr(0); i < 1<<20; i++ {
		b := *(*byte)(unsafe.Add(ptr, i))
		if b == 0 {
			break
		}
		out = append(out, b)
	}
	return string(out)
}

func loadSDL() (sdl, uintptr, error) {
	lib, err := purego.Dlopen("libSDL2-2.0.so.0", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return sdl{}, 0, fmt.Errorf("SDL2 runtime missing: %w", err)
	}
	var s sdl
	for _, binding := range []struct {
		target any
		name   string
	}{
		{&s.init, "SDL_Init"}, {&s.quit, "SDL_Quit"}, {&s.createWindow, "SDL_CreateWindow"}, {&s.destroyWindow, "SDL_DestroyWindow"},
		{&s.glSetAttribute, "SDL_GL_SetAttribute"}, {&s.glCreateContext, "SDL_GL_CreateContext"},
		{&s.glDeleteContext, "SDL_GL_DeleteContext"}, {&s.glGetProcAddress, "SDL_GL_GetProcAddress"},
		{&s.glSwapWindow, "SDL_GL_SwapWindow"}, {&s.glSetSwapInterval, "SDL_GL_SetSwapInterval"},
		{&s.glGetDrawableSize, "SDL_GL_GetDrawableSize"}, {&s.pollEvent, "SDL_PollEvent"},
		{&s.startTextInput, "SDL_StartTextInput"}, {&s.stopTextInput, "SDL_StopTextInput"}, {&s.getError, "SDL_GetError"},
		{&s.getClipboardText, "SDL_GetClipboardText"}, {&s.free, "SDL_free"}, {&s.setWindowFullscreen, "SDL_SetWindowFullscreen"},
		{&s.showCursor, "SDL_ShowCursor"},
	} {
		purego.RegisterLibFunc(binding.target, lib, binding.name)
	}
	return s, lib, nil
}

func loadGL(s sdl) (gl, error) {
	var g gl
	for _, binding := range []struct {
		target any
		name   string
	}{
		{&g.clearColor, "glClearColor"}, {&g.clear, "glClear"}, {&g.enable, "glEnable"}, {&g.disable, "glDisable"}, {&g.viewport, "glViewport"},
		{&g.createShader, "glCreateShader"}, {&g.shaderSource, "glShaderSource"}, {&g.compileShader, "glCompileShader"},
		{&g.getShaderiv, "glGetShaderiv"}, {&g.getShaderInfoLog, "glGetShaderInfoLog"}, {&g.deleteShader, "glDeleteShader"},
		{&g.createProgram, "glCreateProgram"}, {&g.attachShader, "glAttachShader"}, {&g.linkProgram, "glLinkProgram"},
		{&g.getProgramiv, "glGetProgramiv"}, {&g.getProgramInfoLog, "glGetProgramInfoLog"}, {&g.useProgram, "glUseProgram"}, {&g.deleteProgram, "glDeleteProgram"},
		{&g.getUniformLocation, "glGetUniformLocation"}, {&g.uniformMatrix4fv, "glUniformMatrix4fv"}, {&g.uniform1i, "glUniform1i"}, {&g.uniform2f, "glUniform2f"}, {&g.uniform1f, "glUniform1f"},
		{&g.genVertexArrays, "glGenVertexArrays"}, {&g.bindVertexArray, "glBindVertexArray"}, {&g.deleteVertexArrays, "glDeleteVertexArrays"},
		{&g.genBuffers, "glGenBuffers"}, {&g.bindBuffer, "glBindBuffer"}, {&g.bufferData, "glBufferData"}, {&g.deleteBuffers, "glDeleteBuffers"},
		{&g.enableVertexAttribArray, "glEnableVertexAttribArray"}, {&g.vertexAttribPointer, "glVertexAttribPointer"},
		{&g.drawElements, "glDrawElements"}, {&g.drawArrays, "glDrawArrays"},
		{&g.genTextures, "glGenTextures"}, {&g.activeTexture, "glActiveTexture"}, {&g.bindTexture, "glBindTexture"},
		{&g.texImage2D, "glTexImage2D"}, {&g.texParameteri, "glTexParameteri"}, {&g.deleteTextures, "glDeleteTextures"},
		{&g.blendFunc, "glBlendFunc"}, {&g.depthMask, "glDepthMask"}, {&g.getString, "glGetString"}, {&g.getError, "glGetError"}, {&g.readPixels, "glReadPixels"},
	} {
		addr := s.glGetProcAddress(binding.name)
		if addr == 0 {
			return g, fmt.Errorf("OpenGL entry point %s unavailable", binding.name)
		}
		purego.RegisterFunc(binding.target, addr)
	}
	return g, nil
}

const vertexShader = `#version 330 core
layout(location=0) in vec3 position;
layout(location=1) in vec3 normal;
layout(location=2) in vec2 uv;
layout(location=3) in vec4 weights;
layout(location=4) in vec4 bones;
uniform mat4 mvp;
uniform mat4 world;
uniform mat4 model;
uniform sampler2D boneTexture;
uniform int skinEnabled;
out vec2 texcoord;
out vec3 lightingNormal;
mat4 bone(int b) {
    return mat4(texelFetch(boneTexture,ivec2(0,b),0),texelFetch(boneTexture,ivec2(1,b),0),
                texelFetch(boneTexture,ivec2(2,b),0),texelFetch(boneTexture,ivec2(3,b),0));
}
void main() {
    mat4 skin=mat4(1.0);
    float total=dot(weights,vec4(1));
    if(skinEnabled!=0 && total>0.0) {
        skin=(bone(int(bones.x))*weights.x+bone(int(bones.y))*weights.y+
              bone(int(bones.z))*weights.z+bone(int(bones.w))*weights.w)/total;
    }
    gl_Position=mvp*model*skin*vec4(position,1);
    lightingNormal=mat3(world)*mat3(model)*mat3(skin)*normal;
    texcoord=uv;
}`
const fragmentShader = `#version 330 core
in vec2 texcoord;
in vec3 lightingNormal;
uniform sampler2D diffuse;
uniform float alphaCutoff;
uniform int unlit;
out vec4 color;
void main() {
    vec4 tex=texture(diffuse,texcoord);
    if(tex.a<alphaCutoff) discard;
    vec3 n=normalize(lightingNormal);
    float light=0.45+0.55*max(0.0,dot(n,normalize(vec3(0.3,0.6,1.0))))+0.2*max(0.0,dot(n,normalize(vec3(-0.6,0.2,-0.5))));
    if(unlit!=0) light=1.0;
    color=vec4(tex.rgb*light,tex.a);
}`
const overlayVertex = `#version 330 core
uniform vec2 scale;
out vec2 uv;
void main() {
    vec2 p=vec2((gl_VertexID<<1)&2,gl_VertexID&2);
    uv=0.5+(vec2(p.x,1.0-p.y)-0.5)*scale;
    gl_Position=vec4(p*2.0-1.0,0,1);
}`
const overlayFragment = `#version 330 core
in vec2 uv;
uniform sampler2D diffuse;
out vec4 color;
void main(){color=texture(diffuse,uv);}`

func (g gl) program(vertex, fragment string) (uint32, error) {
	program := g.createProgram()
	for _, source := range []struct {
		kind  uint32
		value string
	}{{0x8B31, vertex}, {0x8B30, fragment}} {
		shader := g.createShader(source.kind)
		bytes := append([]byte(source.value), 0)
		p := unsafe.Pointer(&bytes[0])
		g.shaderSource(shader, 1, unsafe.Pointer(&p), nil)
		g.compileShader(shader)
		runtime.KeepAlive(bytes)
		var ok int32
		g.getShaderiv(shader, 0x8B81, &ok)
		if ok == 0 {
			log := make([]byte, 8192)
			g.getShaderInfoLog(shader, int32(len(log)), nil, unsafe.Pointer(&log[0]))
			g.deleteShader(shader)
			g.deleteProgram(program)
			return 0, fmt.Errorf("shader compile failed: %s", strings.TrimRight(string(log), "\x00"))
		}
		g.attachShader(program, shader)
		g.deleteShader(shader)
	}
	g.linkProgram(program)
	var ok int32
	g.getProgramiv(program, 0x8B82, &ok)
	if ok == 0 {
		log := make([]byte, 8192)
		g.getProgramInfoLog(program, int32(len(log)), nil, unsafe.Pointer(&log[0]))
		g.deleteProgram(program)
		return 0, fmt.Errorf("shader link failed: %s", strings.TrimRight(string(log), "\x00"))
	}
	return program, nil
}

func (g gl) texture(img *image.NRGBA) uint32 {
	var texture uint32
	g.genTextures(1, &texture)
	g.bindTexture(0x0DE1, texture)
	g.texParameteri(0x0DE1, 0x2801, 0x2601)
	g.texParameteri(0x0DE1, 0x2800, 0x2601)
	g.texParameteri(0x0DE1, 0x2802, 0x812F)
	g.texParameteri(0x0DE1, 0x2803, 0x812F)
	g.texImage2D(0x0DE1, 0, 0x8058, int32(img.Rect.Dx()), int32(img.Rect.Dy()), 0, 0x1908, 0x1401, unsafe.Pointer(&img.Pix[0]))
	runtime.KeepAlive(img)
	return texture
}

type mesh struct {
	vao, vbo, ibo, bones uint32
	textures             []uint32 // per model texture slot; 0 = skip batches using it
	instance             *model.Instance
}

// textureCache shares GL textures between instances; composited character
// textures are the same *image.NRGBA for body, attachments and collections.
type textureCache map[*image.NRGBA]uint32

func (g gl) upload(in *model.Instance, cache textureCache) mesh {
	a := in.Asset
	out := mesh{instance: in}
	g.genVertexArrays(1, &out.vao)
	g.bindVertexArray(out.vao)
	g.genBuffers(1, &out.vbo)
	g.bindBuffer(0x8892, out.vbo)
	vertices := make([]float32, 0, len(a.Model.Vertices)*16+1)
	for _, v := range a.Model.Vertices {
		vertices = append(vertices, v.Position[:]...)
		vertices = append(vertices, v.Normal[:]...)
		vertices = append(vertices, v.UV[:]...)
		for _, weight := range v.Weights {
			vertices = append(vertices, float32(weight)/255)
		}
		for _, bone := range v.Bones {
			vertices = append(vertices, float32(bone))
		}
	}
	if len(vertices) > 0 {
		g.bufferData(0x8892, int64(len(vertices)*4), unsafe.Pointer(&vertices[0]), 0x88E4)
	}
	offset := uintptr(0)
	for i, size := range []int32{3, 3, 2, 4, 4} {
		g.enableVertexAttribArray(uint32(i))
		g.vertexAttribPointer(uint32(i), size, 0x1406, 0, 64, offset)
		offset += uintptr(size) * 4
	}
	g.genBuffers(1, &out.ibo)
	g.bindBuffer(0x8893, out.ibo)
	if len(a.Model.Indices) > 0 {
		g.bufferData(0x8893, int64(len(a.Model.Indices)*4), unsafe.Pointer(&a.Model.Indices[0]), 0x88E4)
	}
	runtime.KeepAlive(vertices)
	runtime.KeepAlive(a)
	g.genTextures(1, &out.bones)
	g.activeTexture(0x84C1)
	g.bindTexture(0x0DE1, out.bones)
	g.texParameteri(0x0DE1, 0x2801, 0x2600)
	g.texParameteri(0x0DE1, 0x2800, 0x2600)
	g.activeTexture(0x84C0)
	for i, img := range a.Textures {
		if img == nil || img.Rect.Empty() {
			// Unresolved replaceable textures (e.g. unused glow/hair slots) are
			// not drawn rather than shown as flat placeholder geometry.
			out.textures = append(out.textures, 0)
			continue
		}
		tex, ok := cache[img]
		if !ok {
			tex = g.texture(img)
			cache[img] = tex
		} else {
			g.bindTexture(0x0DE1, tex)
		}
		wrapS, wrapT := int32(0x812F), int32(0x812F)
		if a.Model.Textures[i].Flags&1 != 0 {
			wrapS = 0x2901
		}
		if a.Model.Textures[i].Flags&2 != 0 {
			wrapT = 0x2901
		}
		g.texParameteri(0x0DE1, 0x2802, wrapS)
		g.texParameteri(0x0DE1, 0x2803, wrapT)
		out.textures = append(out.textures, tex)
	}
	return out
}

func (g gl) freeMesh(m mesh) {
	g.deleteVertexArrays(1, &m.vao)
	g.deleteBuffers(1, &m.vbo)
	g.deleteBuffers(1, &m.ibo)
	g.deleteTextures(1, &m.bones)
}

type sceneGPU struct {
	scene  *model.Scene
	meshes []mesh
	cache  textureCache
	center model.Vec3
	radius float32
}

func (g gl) uploadScene(sc *model.Scene) (*sceneGPU, error) {
	out := &sceneGPU{scene: sc, cache: textureCache{}}
	for _, in := range sc.Instances {
		out.meshes = append(out.meshes, g.upload(in, out.cache))
	}
	lo, hi := sc.Bounds(0)
	out.center = model.Vec3{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	out.radius = float32(math.Sqrt(float64((hi[0]-lo[0])*(hi[0]-lo[0])+(hi[1]-lo[1])*(hi[1]-lo[1])+(hi[2]-lo[2])*(hi[2]-lo[2])))) / 2
	if !(out.radius > 0.001) || math.IsInf(float64(out.radius), 0) {
		out.radius = 1
	}
	return out, nil
}

func (g gl) freeScene(sg *sceneGPU) {
	if sg == nil {
		return
	}
	for _, m := range sg.meshes {
		g.freeMesh(m)
	}
	for _, t := range sg.cache {
		g.deleteTextures(1, &t)
	}
}

func Run(config Config) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	s, lib, err := loadSDL()
	if err != nil {
		return err
	}
	defer purego.Dlclose(lib)
	if s.init(0x20) != 0 {
		return fmt.Errorf("SDL initialization: %s", cString(s.getError()))
	}
	defer s.quit()
	for _, attribute := range [][2]int32{{17, 3}, {18, 3}, {21, 1}, {5, 1}, {6, 24}} {
		if s.glSetAttribute(attribute[0], attribute[1]) != 0 {
			return fmt.Errorf("OpenGL attributes: %s", cString(s.getError()))
		}
	}
	flags := uint32(0x2 | 0x20 | 0x2000)
	if config.Fullscreen {
		flags |= 0x1001
	}
	window := s.createWindow("Armory - native Go / direct Blizzard CDN", 0x2fff0000, 0x2fff0000, 1280, 800, flags)
	if window == 0 {
		return fmt.Errorf("SDL window: %s", cString(s.getError()))
	}
	defer s.destroyWindow(window)
	context := s.glCreateContext(window)
	if context == 0 {
		return fmt.Errorf("OpenGL 3.3 context: %s", cString(s.getError()))
	}
	defer s.glDeleteContext(context)
	s.glSetSwapInterval(1)
	g, err := loadGL(s)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "OpenGL: %s / %s\n", cString(g.getString(0x1F02)), cString(g.getString(0x1F01)))
	program, err := g.program(vertexShader, fragmentShader)
	if err != nil {
		return err
	}
	defer g.deleteProgram(program)
	overlay, err := g.program(overlayVertex, overlayFragment)
	if err != nil {
		return err
	}
	defer g.deleteProgram(overlay)
	var overlayVAO, overlayTexture uint32
	g.genVertexArrays(1, &overlayVAO)
	defer g.deleteVertexArrays(1, &overlayVAO)
	g.genTextures(1, &overlayTexture)
	defer g.deleteTextures(1, &overlayTexture)
	g.bindTexture(0x0DE1, overlayTexture)
	g.texParameteri(0x0DE1, 0x2801, 0x2600)
	g.texParameteri(0x0DE1, 0x2800, 0x2600)
	g.texParameteri(0x0DE1, 0x2802, 0x812F)
	g.texParameteri(0x0DE1, 0x2803, 0x812F)
	var background uint32
	if config.Background != nil {
		background = g.texture(config.Background)
		defer g.deleteTextures(1, &background)
	}
	var current *sceneGPU
	defer func() { g.freeScene(current) }()
	if config.Scene != nil {
		current, err = g.uploadScene(config.Scene)
		if err != nil {
			return err
		}
		m := config.Scene.Root()
		if m.Stand >= 0 {
			duration := m.Sequences[m.Stand].Duration
			first, err := m.Pose(0)
			if err != nil {
				return err
			}
			middle, err := m.Pose(duration / 2)
			if err != nil {
				return err
			}
			changed := false
			for i := range first {
				if first[i] != middle[i] {
					changed = true
					break
				}
			}
			fmt.Fprintf(os.Stderr, "Stand: %d ms, %d bones, animated pose changes: %t\n", duration, len(m.Bones), changed)
			seam := func(clip int, t uint32) float32 {
				pose, err := m.PoseBlend(model.Blend{From: model.ClipTime{Clip: clip, Time: t}}, 0, nil)
				if err != nil {
					return -1
				}
				var worst float32
				for i := range pose {
					for _, k := range []int{12, 13, 14} {
						worst = max(worst, abs32(pose[i][k]-first[i][k]))
					}
				}
				return worst
			}
			for i, c := range m.Idles {
				fmt.Fprintf(os.Stderr, "Idle %d (variation %d): %d ms, seam start %.3f end %.3f\n", i+1, m.Sequences[c.Sequence].Variation, c.Duration, seam(i+1, 0), seam(i+1, c.Duration-1))
			}
		}
	}
	g.clearColor(0.025, 0.032, 0.045, 1)
	start := time.Now()
	input, status := "", config.Status
	if status == "" {
		status = "Paste a retail Blizzard Armory URL. Enter: look up | F11: fullscreen | Esc: quit"
	}
	help, showUI, paused, busy := false, config.Scene == nil, false, false
	cursorShown, lookupStart := true, time.Now()
	angle, zoom := float32(-math.Pi/2), float32(1)
	type lookupResult struct {
		scene *model.Scene
		err   error
	}
	results := make(chan lookupResult, 1)
	lookup := func(raw string) {
		busy, showUI, lookupStart = true, true, time.Now()
		if config.Progress != nil {
			config.Progress.Reset()
		}
		status = "Fetching Armory appearance and assembling character from the Blizzard CDN..."
		go func() {
			sc, err := config.Lookup(raw)
			results <- lookupResult{sc, err}
		}()
	}
	if config.URL != "" && config.Lookup != nil {
		input = config.URL
		lookup(input)
	}
	s.startTextInput()
	defer s.stopTextInput()
	var pauseStart time.Time
	var pausedDuration time.Duration
	for {
		event := [56]byte{}
		for s.pollEvent(unsafe.Pointer(&event[0])) != 0 {
			kind := modelEndian(event[:4])
			if kind == 0x100 {
				return nil
			}
			if kind == 0x303 && showUI {
				text := string(event[12:44])
				text, _, _ = strings.Cut(text, "\x00")
				if len(input)+len(text) <= 2048 {
					input += text
				}
			}
			if kind == 0x300 && event[13] == 0 {
				key := int32(modelEndian(event[20:24]))
				mods := uint16(event[24]) | uint16(event[25])<<8
				switch key {
				case 27:
					return nil
				case 8:
					runes := []rune(input)
					if len(runes) > 0 {
						input = string(runes[:len(runes)-1])
					}
				case 13:
					if showUI && !busy && config.Lookup != nil && input != "" {
						lookup(input)
					}
				case 118:
					if mods&0xc0 != 0 {
						ptr := s.getClipboardText()
						text := cString(ptr)
						s.free(ptr)
						if len(input)+len(text) <= 2048 {
							input += strings.TrimSpace(text)
						}
					}
				case 97:
					if mods&0xc0 != 0 {
						input = ""
					}
				case 1073741892:
					config.Fullscreen = !config.Fullscreen
					flags := uint32(0)
					if config.Fullscreen {
						flags = 0x1001
					}
					if s.setWindowFullscreen(window, flags) != 0 {
						status = "Fullscreen: " + cString(s.getError())
					}
				case 1073741904:
					angle -= 0.08
				case 1073741903:
					angle += 0.08
				case 1073741906:
					zoom = max(0.3, zoom-0.1)
				case 1073741905:
					zoom = min(4, zoom+0.1)
				case 32:
					if !showUI {
						paused = !paused
						if paused {
							pauseStart = time.Now()
						} else {
							pausedDuration += time.Since(pauseStart)
						}
					}
				case 9:
					showUI = !showUI
				case 63:
					help = !help
				}
				if key == 47 && mods&3 != 0 {
					help = !help
				}
				if key == 113 && !showUI {
					return nil
				}
			}
		}
		select {
		case result := <-results:
			busy = false
			if result.err != nil {
				status = result.err.Error()
				break
			}
			next, err := g.uploadScene(result.scene)
			if err != nil {
				status = err.Error()
				break
			}
			g.freeScene(current)
			current, config.Scene = next, result.scene
			status = "Loaded " + result.scene.Title
			showUI, help, angle, zoom = false, false, float32(-math.Pi/2), 1
			start, pausedDuration, paused = time.Now(), 0, false
		default:
		}
		var width, height int32
		s.glGetDrawableSize(window, &width, &height)
		if width <= 0 || height <= 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		g.viewport(0, 0, width, height)
		g.clear(0x4000 | 0x100)
		if background != 0 {
			g.drawBackground(overlay, overlayVAO, background, config.Background.Rect, width, height)
		}
		if current != nil {
			elapsed := time.Since(start) - pausedDuration
			if paused {
				elapsed = pauseStart.Sub(start) - pausedDuration
			}
			if err := g.drawScene(program, current, uint32(elapsed.Milliseconds()), float32(width)/float32(height), angle, zoom); err != nil {
				return err
			}
		}
		// Everything (overlay and mouse cursor) disappears once a character is shown.
		if visible := int32(0); (showUI || help) != cursorShown {
			cursorShown = showUI || help
			if cursorShown {
				visible = 1
			}
			s.showCursor(visible)
		}
		if showUI || help {
			var foot *footer
			if busy && config.Progress != nil {
				foot = &footer{}
				foot.lines, foot.summary, foot.fraction = config.Progress.snapshot(int(height) / 32)
				foot.phase = time.Since(lookupStart).Seconds()
			}
			lines := []string{"ARMORY / native Go + OpenGL", "", "URL: " + input, "", status}
			if config.Scene != nil {
				lines = append(lines, config.Scene.Warnings...)
			}
			if help {
				lines = append(lines, "", "Tab: hide/show input | Ctrl+V: paste | Ctrl+A: clear input", "Enter: profile lookup | F11: fullscreen | Esc: quit", "With input hidden: arrows orbit/zoom | Space: pause | Q: quit", "?: shortcuts | Idle: Stand (WoW animation 0) cross-fading into its fidget variations.")
			}
			g.drawOverlay(overlay, overlayVAO, overlayTexture, int(width), int(height), lines, foot)
		}
		if code := g.getError(); code != 0 {
			return fmt.Errorf("OpenGL error 0x%x", code)
		}
		if config.SelfTest && time.Since(start) > 500*time.Millisecond {
			if config.Screenshot != "" {
				if err := g.screenshot(width, height, config.Screenshot); err != nil {
					return err
				}
			}
			fmt.Fprintln(os.Stderr, "Native shader/window smoke test passed.")
			return nil
		}
		s.glSwapWindow(window)
		time.Sleep(time.Millisecond)
	}
}

func modelEndian(data []byte) uint32 {
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
}

// blendFuncs maps M2 material blend modes to GL source/destination factors.
var blendFuncs = [...][2]uint32{
	2: {0x0302, 0x0303}, // alpha
	3: {1, 1},           // add
	4: {0x0302, 1},      // alpha add
	5: {0x0306, 0},      // modulate
	6: {0x0306, 0x0300}, // modulate 2x
	7: {1, 0x0303},      // premultiplied
}

func (g gl) drawScene(program uint32, sg *sceneGPU, ms uint32, aspect, angle, zoom float32) error {
	frame, err := sg.scene.Animate(ms)
	if err != nil {
		return err
	}
	g.enable(0x0B71)
	g.depthMask(1)
	g.disable(0x0BE2)
	g.useProgram(program)
	g.uniform1i(g.getUniformLocation(program, "diffuse"), 0)
	g.uniform1i(g.getUniformLocation(program, "boneTexture"), 1)
	rotation := model.Multiply(model.Rotation(model.Quat{float32(math.Sin(-math.Pi / 4)), 0, 0, float32(math.Cos(-math.Pi / 4))}),
		model.Rotation(model.Quat{0, 0, float32(math.Sin(float64(angle) / 2)), float32(math.Cos(float64(angle) / 2))}))
	world := model.Multiply(rotation, model.Translate(model.Vec3{-sg.center[0], -sg.center[1], -sg.center[2]}))
	// Fit the bounding sphere vertically, widening for portrait windows.
	distance := sg.radius * 2.2 * zoom / max(0.25, min(1, aspect))
	view := model.Translate(model.Vec3{0, 0, -distance})
	mvp := model.Multiply(model.Perspective(aspect, max(0.01, sg.radius*0.01), sg.radius*100), model.Multiply(view, world))
	g.uniformMatrix4fv(g.getUniformLocation(program, "mvp"), 1, 0, unsafe.Pointer(&mvp[0]))
	g.uniformMatrix4fv(g.getUniformLocation(program, "world"), 1, 0, unsafe.Pointer(&world[0]))
	modelLoc, skinLoc := g.getUniformLocation(program, "model"), g.getUniformLocation(program, "skinEnabled")
	cutoffLoc, unlitLoc := g.getUniformLocation(program, "alphaCutoff"), g.getUniformLocation(program, "unlit")
	for i, me := range sg.meshes {
		pose := frame.Poses[i]
		if len(pose) > 0 {
			g.activeTexture(0x84C1)
			g.bindTexture(0x0DE1, me.bones)
			g.texImage2D(0x0DE1, 0, 0x8814, 4, int32(len(pose)), 0, 0x1908, 0x1406, unsafe.Pointer(&pose[0][0]))
			runtime.KeepAlive(pose)
			g.activeTexture(0x84C0)
		}
	}
	for pass := 0; pass < 2; pass++ {
		for i, me := range sg.meshes {
			m := me.instance.Asset.Model
			mm := frame.Models[i]
			g.uniformMatrix4fv(modelLoc, 1, 0, unsafe.Pointer(&mm[0]))
			skin := int32(0)
			if len(frame.Poses[i]) > 0 {
				skin = 1
				g.activeTexture(0x84C1)
				g.bindTexture(0x0DE1, me.bones)
				g.activeTexture(0x84C0)
			}
			g.uniform1i(skinLoc, skin)
			g.bindVertexArray(me.vao)
			for _, batch := range m.Batches {
				if !me.instance.Visible(batch.Geoset) || batch.Texture < 0 || batch.Texture >= len(me.textures) || me.textures[batch.Texture] == 0 {
					continue
				}
				transparent := batch.Blend >= 2
				if transparent != (pass == 1) {
					continue
				}
				if transparent && int(batch.Blend) < len(blendFuncs) {
					f := blendFuncs[batch.Blend]
					g.enable(0x0BE2)
					g.blendFunc(f[0], f[1])
					g.depthMask(0)
				} else {
					g.disable(0x0BE2)
					g.depthMask(1)
				}
				if batch.Flags&0x10 != 0 {
					g.depthMask(0)
				}
				cutoff := float32(0)
				if batch.Blend == 1 {
					cutoff = 0.5
				} else if transparent {
					cutoff = 1.0 / 255
				}
				g.uniform1f(cutoffLoc, cutoff)
				unlit := int32(0)
				if batch.Flags&0x01 != 0 {
					unlit = 1
				}
				g.uniform1i(unlitLoc, unlit)
				g.bindTexture(0x0DE1, me.textures[batch.Texture])
				g.drawElements(0x0004, int32(batch.Count), 0x1405, uintptr(batch.Start*4))
			}
		}
	}
	g.disable(0x0BE2)
	g.depthMask(1)
	return nil
}

// footer is the download log and progress bar drawn at the window bottom.
type footer struct {
	lines    []string
	summary  string
	fraction float64 // < 0: indeterminate
	phase    float64 // seconds, animates the indeterminate bar
}

func printable(line string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		if r > 126 {
			return '?'
		}
		return r
	}, line)
}

func reversed(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func (g gl) drawOverlay(program, vao, texture uint32, width, height int, lines []string, foot *footer) {
	// Rasterize text in a small logical surface; OpenGL composites it at display size.
	w, h := max(1, width/2), max(1, height/2)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	drawer := font.Drawer{Dst: img, Src: image.NewUniform(color.NRGBA{225, 229, 235, 255}), Face: basicfont.Face7x13}
	limit := max(4, (w-24)/7)
	bottom := h - 10 // text area of the top block ends here
	if foot != nil {
		bottom = h - 16 - 6 - 2*15 // keep room for the summary and one URL
	}
	y := 20
	for _, line := range lines {
		line = printable(line)
		for {
			n := min(len(line), limit)
			text := line[:n]
			drawer.Dot = fixed.P(12, y)
			drawer.DrawString(text)
			y += 16
			line = line[n:]
			if line == "" || y >= bottom {
				break
			}
		}
		if y >= bottom {
			break
		}
	}
	topEnd := y - 6
	if foot != nil {
		barTop := h - 16
		fill := func(x0, x1 int, c color.NRGBA) {
			draw.Draw(img, image.Rect(x0, barTop, x1, h-8), image.NewUniform(c), image.Point{}, draw.Src)
		}
		x0, x1 := 12, w-12
		fill(x0, x1, color.NRGBA{40, 48, 60, 220})
		if foot.fraction >= 0 {
			fill(x0, x0+int(float64(x1-x0)*min(1, foot.fraction)), color.NRGBA{64, 156, 255, 255})
		} else if x1-x0 > 4 {
			seg := (x1 - x0) / 5
			off := int(foot.phase*float64(x1-x0)/1.5) % (x1 - x0 + seg)
			fill(max(x0, x0+off-seg), min(x1, x0+off), color.NRGBA{64, 156, 255, 255})
		}
		y := barTop - 6
		rows := append([]string{foot.summary}, reversed(foot.lines)...)
		dim := image.NewUniform(color.NRGBA{150, 160, 175, 255})
		for k, line := range rows {
			if y-13 < topEnd {
				break
			}
			line = printable(line)
			if len(line) > limit {
				// Keep host and the hash/size tail of long CDN URLs.
				head := min(28, limit/3)
				line = line[:head] + "..." + line[len(line)-(limit-head-3):]
			}
			if k > 0 {
				drawer.Src = dim
			}
			drawer.Dot = fixed.P(12, y)
			drawer.DrawString(line)
			y -= 15
		}
		drawer.Src = image.NewUniform(color.NRGBA{225, 229, 235, 255})
	}
	g.disable(0x0B71)
	g.enable(0x0BE2)
	g.blendFunc(0x0302, 0x0303)
	g.activeTexture(0x84C0)
	g.bindTexture(0x0DE1, texture)
	g.texImage2D(0x0DE1, 0, 0x8058, int32(w), int32(h), 0, 0x1908, 0x1401, unsafe.Pointer(&img.Pix[0]))
	runtime.KeepAlive(img)
	g.useProgram(program)
	g.uniform1i(g.getUniformLocation(program, "diffuse"), 0)
	g.uniform2f(g.getUniformLocation(program, "scale"), 1, 1)
	g.bindVertexArray(vao)
	g.drawArrays(0x0004, 0, 3)
}

// drawBackground fills the window with img, cropping the longer side
// ("cover") so the image is never distorted.
func (g gl) drawBackground(program, vao, texture uint32, img image.Rectangle, width, height int32) {
	sx, sy := float32(1), float32(1)
	if ia, wa := float32(img.Dx())/float32(img.Dy()), float32(width)/float32(height); ia > wa {
		sx = wa / ia
	} else {
		sy = ia / wa
	}
	g.disable(0x0B71)
	g.disable(0x0BE2)
	g.activeTexture(0x84C0)
	g.bindTexture(0x0DE1, texture)
	g.useProgram(program)
	g.uniform1i(g.getUniformLocation(program, "diffuse"), 0)
	g.uniform2f(g.getUniformLocation(program, "scale"), sx, sy)
	g.bindVertexArray(vao)
	g.drawArrays(0x0004, 0, 3)
}

func (g gl) screenshot(width, height int32, path string) error {
	img := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	g.readPixels(0, 0, width, height, 0x1908, 0x1401, unsafe.Pointer(&img.Pix[0]))
	row := img.Stride
	temp := make([]byte, row)
	for y := 0; y < int(height)/2; y++ {
		top, bottom := img.Pix[y*row:(y+1)*row], img.Pix[(int(height)-1-y)*row:(int(height)-y)*row]
		copy(temp, top)
		copy(top, bottom)
		copy(bottom, temp)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
