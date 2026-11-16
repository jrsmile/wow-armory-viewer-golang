//go:build linux || (windows && amd64)

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
	"sort"
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
	setMainReady        func()
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
	uniform3f               func(int32, float32, float32, float32)
	uniform3fv              func(int32, int32, unsafe.Pointer)
	uniform4f               func(int32, float32, float32, float32, float32)
	cullFace                func(uint32)
	frontFace               func(uint32)
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
	lib, err := openSDL()
	if err != nil {
		return sdl{}, 0, fmt.Errorf("SDL2 runtime missing: %w", err)
	}
	var s sdl
	for _, binding := range []struct {
		target any
		name   string
	}{
		{&s.setMainReady, "SDL_SetMainReady"},
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
		{&g.uniform3f, "glUniform3f"}, {&g.uniform3fv, "glUniform3fv"}, {&g.uniform4f, "glUniform4f"}, {&g.cullFace, "glCullFace"}, {&g.frontFace, "glFrontFace"},
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

// The model shader ports the client's M2 material model as reimplemented by
// WebWowViewerCpp (wow.tools): per-batch vertex shaders generate texture
// coordinates (UV sets, texture animation, sphere environment mapping, edge
// fade) and pixel shaders combine up to four textures into a diffuse colour
// plus an additive glow that is not affected by lighting. Lighting follows
// Wowhead's character viewer: ambient plus one key and two fill lights in
// view space, clamped, with the glow added afterwards.
const vertexShader = `#version 330 core
layout(location=0) in vec3 position;
layout(location=1) in vec3 normal;
layout(location=2) in vec2 uv;
layout(location=3) in vec4 weights;
layout(location=4) in vec4 bones;
layout(location=5) in vec2 uv2;
uniform mat4 projection;
uniform mat4 view;
uniform mat4 model;
uniform sampler2D boneTexture;
uniform int skinEnabled;
out vec2 texcoord1;
out vec2 texcoord2;
out vec3 viewNormal;
out vec3 viewPosition;
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
    mat4 viewModel=view*model*skin;
    vec4 p=viewModel*vec4(position,1);
    gl_Position=projection*p;
    viewPosition=p.xyz;
    viewNormal=mat3(viewModel)*normal;
    texcoord1=uv;
    texcoord2=uv2;
}`
const fragmentShader = `#version 330 core
in vec2 texcoord1;
in vec2 texcoord2;
in vec3 viewNormal;
in vec3 viewPosition;
uniform sampler2D texture1;
uniform sampler2D texture2;
uniform sampler2D texture3;
uniform sampler2D texture4;
uniform int vertexShader;
uniform int pixelShader;
uniform int blendMode;
uniform int unlit;
uniform mat4 textureMatrix[2];
uniform vec4 meshColor;
uniform vec3 textureWeight;
uniform vec3 ambientColor;
uniform vec3 keyColor;
uniform vec3 fillColor;
uniform vec3 lightDir[3];
out vec4 color;

vec2 envCoord(vec3 pos, vec3 n) {
    vec3 r=reflect(normalize(pos),n);
    return -(normalize(vec3(r.xy,r.z+1.0)).xy*0.5)+vec2(0.5);
}
float edgeScan(vec3 pos, vec3 n) {
    float d=clamp(dot(-normalize(pos),n),0.0,1.0);
    return clamp(2.7*d*d-0.4,0.0,1.0);
}
vec2 t1() { return (textureMatrix[0]*vec4(texcoord1,0,1)).xy; }
vec2 t2() { return (textureMatrix[1]*vec4(texcoord2,0,1)).xy; }

void main() {
    vec3 n=normalize(viewNormal);
    vec2 env=envCoord(viewPosition,n);
    vec2 uv1=vec2(0), uv2=vec2(0), uv3=vec2(0);
    float edgeFade=1.0;
    switch(vertexShader) {
    case 0: uv1=t1(); break;
    case 1: uv1=env; break;
    case 2: uv1=t1(); uv2=t2(); break;
    case 3: uv1=t1(); uv2=env; break;
    case 4: uv1=env; uv2=t1(); break;
    case 5: uv1=env; uv2=env; break;
    case 6: uv1=t1(); uv2=env; uv3=t1(); break;
    case 7: uv1=t1(); uv2=t1(); break;
    case 8: uv1=t1(); uv2=t1(); uv3=t1(); break;
    case 9: edgeFade=edgeScan(viewPosition,n); uv1=t1(); break;
    case 10: uv1=t2(); break;
    case 11: uv1=t1(); uv2=env; uv3=t2(); break;
    case 12: edgeFade=edgeScan(viewPosition,n); uv1=t1(); uv2=t2(); break;
    case 13: edgeFade=edgeScan(viewPosition,n); uv1=env; break;
    case 14: uv1=t1(); uv2=t2(); uv3=t1(); break;
    case 15: uv1=t1(); uv2=t2(); break;
    case 16: uv1=t2(); break;
    default: uv1=t1(); break;
    }
    vec4 mesh=meshColor*edgeFade;
    vec3 mc=mesh.rgb;
    vec2 rawUv2=uv2;
    if(pixelShader==26 || pixelShader==28) { uv2=uv1; uv3=uv1; }
    vec4 tex=texture(texture1,uv1);
    vec4 tex2=texture(texture2,uv2);
    vec4 tex3=texture(texture3,uv3);
    vec3 w=textureWeight;
    vec3 diffuse=vec3(0), glow=vec3(0);
    float alpha=1.0;
    bool canDiscard=false;
    switch(pixelShader) {
    case 0: diffuse=mc*tex.rgb; break;
    case 1: diffuse=mc*tex.rgb; alpha=tex.a; canDiscard=true; break;
    case 2: diffuse=mc*tex.rgb*tex2.rgb; alpha=tex2.a; canDiscard=true; break;
    case 3: diffuse=mc*tex.rgb*tex2.rgb*2.0; alpha=tex2.a*2.0; canDiscard=true; break;
    case 4: diffuse=mc*tex.rgb*tex2.rgb*2.0; break;
    case 5: diffuse=mc*tex.rgb*tex2.rgb; break;
    case 6: diffuse=mc*tex.rgb*tex2.rgb; alpha=tex.a*tex2.a; canDiscard=true; break;
    case 7: diffuse=mc*tex.rgb*tex2.rgb*2.0; alpha=tex.a*tex2.a*2.0; canDiscard=true; break;
    case 8: diffuse=mc*tex.rgb; alpha=tex.a+tex2.a; canDiscard=true; glow=tex2.rgb; break;
    case 9: diffuse=mc*tex.rgb*tex2.rgb*2.0; alpha=tex.a; canDiscard=true; break;
    case 10: diffuse=mc*tex.rgb; alpha=tex.a; canDiscard=true; glow=tex2.rgb; break;
    case 11: diffuse=mc*tex.rgb*tex2.rgb; alpha=tex.a; canDiscard=true; break;
    case 12: diffuse=mc*mix(tex.rgb*tex2.rgb*2.0,tex.rgb,vec3(tex.a)); break;
    case 13: diffuse=mc*tex.rgb; glow=tex2.rgb*tex2.a; break;
    case 14: diffuse=mc*tex.rgb; glow=tex2.rgb*tex2.a*(1.0-tex.a); break;
    case 15: diffuse=mc*mix(tex.rgb*tex2.rgb*2.0,tex.rgb,vec3(tex.a)); glow=tex3.rgb*tex3.a*w.b; break;
    case 16: diffuse=mc*tex.rgb; alpha=tex.a; canDiscard=true; glow=tex2.rgb*tex2.a; break;
    case 17: diffuse=mc*tex.rgb; alpha=tex.a+tex2.a*(0.3*tex2.r+0.59*tex2.g+0.11*tex2.b); canDiscard=true; glow=tex2.rgb*tex2.a*(1.0-tex.a); break;
    case 18: diffuse=mc*mix(mix(tex.rgb,tex2.rgb,vec3(tex2.a)),tex.rgb,vec3(tex.a)); break;
    case 19: diffuse=mc*mix(tex.rgb*tex2.rgb*2.0,tex3.rgb,vec3(tex3.a)); break;
    case 20: diffuse=mc*tex.rgb; glow=tex2.rgb*tex2.a*w.g; break;
    case 21: diffuse=mc*tex.rgb; alpha=tex.a+tex2.a; canDiscard=true; glow=tex2.rgb*(1.0-tex.a); break;
    case 22: diffuse=mc*mix(tex.rgb*tex2.rgb,tex.rgb,vec3(tex.a)); break;
    case 23: diffuse=mc*tex.rgb; alpha=tex.a; canDiscard=true; glow=tex2.rgb*tex2.a*w.g; break;
    case 24: diffuse=mc*mix(tex.rgb,tex2.rgb,vec3(tex2.a)); glow=tex.rgb*tex.a*w.r; break;
    case 25: {
        float g=clamp(tex3.a*w.b,0.0,1.0);
        diffuse=mc*mix(tex.rgb*tex2.rgb*2.0,tex.rgb,vec3(tex.a))*(1.0-g); glow=tex3.rgb*g; break;
    }
    case 26: case 28: {
        vec4 c=mix(mix(tex,tex2,vec4(clamp(w.g,0.0,1.0))),tex3,vec4(clamp(w.b,0.0,1.0)));
        diffuse=mc*c.rgb; alpha=c.a; canDiscard=true;
        if(pixelShader==28) alpha*=texture(texture4,rawUv2).a;
        break;
    }
    case 27: diffuse=mc*mix(mix(tex.rgb*tex2.rgb*2.0,tex3.rgb,vec3(tex3.a)),tex.rgb,vec3(tex.a)); break;
    case 29: diffuse=mc*mix(tex.rgb,tex2.rgb,vec3(tex2.a)); break;
    case 30: case 32:
        diffuse=mc*mix(tex.rgb*mix(vec3(1),tex2.rgb,vec3(tex2.a)),tex3.rgb,vec3(tex3.a));
        if(pixelShader==30) { alpha=tex.a; canDiscard=true; }
        break;
    case 31: diffuse=mc*tex.rgb*mix(vec3(1),tex2.rgb,vec3(tex2.a)); alpha=tex.a; canDiscard=true; break;
    case 33: diffuse=mc*tex.rgb; alpha=tex.a; canDiscard=true; break;
    case 34: alpha=tex.a; canDiscard=true; break;
    case 35: { vec4 c=tex*tex2*tex3; diffuse=mc*c.rgb; alpha=c.a; canDiscard=true; break; }
    case 36: diffuse=mc*tex.rgb*tex2.rgb; alpha=tex.a*tex2.a; canDiscard=true; break;
    default: diffuse=mc*tex.rgb; break;
    }
    float opacity=mesh.a;
    if(blendMode==1) {
        if(canDiscard && alpha<0.501960814) discard;
    } else if(blendMode!=0) {
        opacity=alpha*mesh.a;
    }
    vec3 lit=diffuse;
    if(unlit==0) {
        vec3 light=ambientColor+keyColor*max(0.0,dot(n,lightDir[0]))+
            fillColor*max(0.0,dot(n,lightDir[1]))+fillColor*max(0.0,dot(n,lightDir[2]));
        lit=diffuse*clamp(light,0.0,1.0);
    }
    color=vec4(lit+glow,opacity);
}`

// Particles are pre-expanded view-space quads (see model.ParticleSystem),
// unlit and tinted by their age curves.
const particleVertex = `#version 330 core
layout(location=0) in vec3 position;
layout(location=1) in vec2 uv;
layout(location=2) in vec4 color;
uniform mat4 projection;
out vec2 texCoord;
out vec4 tint;
void main() {
    texCoord=uv;
    tint=color;
    gl_Position=projection*vec4(position,1.0);
}`
const particleFragment = `#version 330 core
in vec2 texCoord;
in vec4 tint;
uniform sampler2D diffuse;
uniform int blendMode;
out vec4 color;
void main() {
    vec4 c=texture(diffuse,texCoord)*tint;
    if(blendMode==1 && c.a<0.501960814) discard;
    if(blendMode<=1) c.a=1.0;
    if(blendMode==3) c.rgb*=c.a; // additive without alpha fades through colour
    color=c;
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
	vertices := make([]float32, 0, len(a.Model.Vertices)*18+1)
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
		vertices = append(vertices, v.UV2[:]...)
	}
	if len(vertices) > 0 {
		g.bufferData(0x8892, int64(len(vertices)*4), unsafe.Pointer(&vertices[0]), 0x88E4)
	}
	offset := uintptr(0)
	for i, size := range []int32{3, 3, 2, 4, 4, 2} {
		g.enableVertexAttribArray(uint32(i))
		g.vertexAttribPointer(uint32(i), size, 0x1406, 0, 72, offset)
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
	// particles holds each mesh's emitter simulation (nil: no emitters),
	// drawn through one streaming vertex buffer.
	particles  []*model.ParticleSystem
	pvao, pvbo uint32
	cache      textureCache
	white      uint32 // bound to unused combiner texture units
	center     model.Vec3
	radius     float32
}

func (g gl) uploadScene(sc *model.Scene) (*sceneGPU, error) {
	out := &sceneGPU{scene: sc, cache: textureCache{}}
	white := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(white.Pix, []byte{255, 255, 255, 255})
	out.white = g.texture(white)
	for _, in := range sc.Instances {
		out.meshes = append(out.meshes, g.upload(in, out.cache))
		var ps *model.ParticleSystem
		if len(in.Asset.Model.Emitters) > 0 {
			ps = model.NewParticleSystem(in.Asset.Model)
		}
		out.particles = append(out.particles, ps)
	}
	g.genVertexArrays(1, &out.pvao)
	g.bindVertexArray(out.pvao)
	g.genBuffers(1, &out.pvbo)
	g.bindBuffer(0x8892, out.pvbo)
	offset := uintptr(0)
	for i, size := range []int32{3, 2, 4} {
		g.enableVertexAttribArray(uint32(i))
		g.vertexAttribPointer(uint32(i), size, 0x1406, 0, 36, offset)
		offset += uintptr(size) * 4
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
	g.deleteVertexArrays(1, &sg.pvao)
	g.deleteBuffers(1, &sg.pvbo)
	g.deleteTextures(1, &sg.white)
}

func Run(config Config) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	s, lib, err := loadSDL()
	if err != nil {
		return err
	}
	defer closeSDL(lib)
	s.setMainReady()
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
	particles, err := g.program(particleVertex, particleFragment)
	if err != nil {
		return err
	}
	defer g.deleteProgram(particles)
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
			if err := g.drawScene(program, particles, current, uint32(elapsed.Milliseconds()), float32(width)/float32(height), angle, zoom); err != nil {
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

// Wowhead's character viewer light rig: ambient plus a white key light and
// two dim fill lights, all fixed in view space so the character stays lit
// from the front while orbiting.
var (
	ambientLight = [3]float32{0.35, 0.35, 0.35}
	keyLight     = [3]float32{1, 1, 1}
	fillLight    = [3]float32{0.35, 0.35, 0.35}
	lightDirs    = normalizedDirs([3][3]float32{{5, -3, 3}, {5, 5, 5}, {-5, -5, -5}})
)

func normalizedDirs(dirs [3][3]float32) [9]float32 {
	var out [9]float32
	for i, d := range dirs {
		l := float32(math.Sqrt(float64(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])))
		for j := range d {
			out[i*3+j] = d[j] / l
		}
	}
	return out
}

type drawItem struct {
	mesh, batch int
	priority    int8
}

// batchTextures resolves the GL textures of a batch's combiner stages. When
// a secondary texture is unavailable the batch falls back to its first
// texture alone so it is still drawn.
func batchTextures(me mesh, b model.Batch, white uint32) (units [4]uint32, pixel, vertex int, ok bool) {
	texture := func(slot int) uint32 {
		if slot < 0 || slot >= len(me.textures) {
			return 0
		}
		return me.textures[slot]
	}
	if units[0] = texture(b.Textures[0]); units[0] == 0 {
		return units, 0, 0, false
	}
	pixel, vertex = b.PixelShader, b.VertexShader
	for j := 1; j < 4; j++ {
		units[j] = white
		if j < min(b.TextureCount, 4) {
			if t := texture(b.Textures[j]); t != 0 {
				units[j] = t
			} else {
				pixel, vertex = 0, 0
				if b.Blend != 0 {
					pixel = 1
				}
			}
		}
	}
	return units, pixel, vertex, true
}

func (g gl) drawScene(program, particles uint32, sg *sceneGPU, ms uint32, aspect, angle, zoom float32) error {
	frame, err := sg.scene.Animate(ms)
	if err != nil {
		return err
	}
	g.enable(0x0B71)
	g.depthMask(1)
	g.disable(0x0BE2)
	g.useProgram(program)
	loc := func(name string) int32 { return g.getUniformLocation(program, name) }
	g.uniform1i(loc("boneTexture"), 1)
	for i, unit := range []int32{0, 2, 3, 4} {
		g.uniform1i(loc(fmt.Sprintf("texture%d", i+1)), unit)
	}
	rotation := model.Multiply(model.Rotation(model.Quat{float32(math.Sin(-math.Pi / 4)), 0, 0, float32(math.Cos(-math.Pi / 4))}),
		model.Rotation(model.Quat{0, 0, float32(math.Sin(float64(angle) / 2)), float32(math.Cos(float64(angle) / 2))}))
	world := model.Multiply(rotation, model.Translate(model.Vec3{-sg.center[0], -sg.center[1], -sg.center[2]}))
	// Fit the bounding sphere vertically, widening for portrait windows.
	distance := sg.radius * 2.2 * zoom / max(0.25, min(1, aspect))
	view := model.Multiply(model.Translate(model.Vec3{0, 0, -distance}), world)
	projection := model.Perspective(aspect, max(0.01, sg.radius*0.01), sg.radius*100)
	g.uniformMatrix4fv(loc("projection"), 1, 0, unsafe.Pointer(&projection[0]))
	g.uniformMatrix4fv(loc("view"), 1, 0, unsafe.Pointer(&view[0]))
	g.uniform3f(loc("ambientColor"), ambientLight[0], ambientLight[1], ambientLight[2])
	g.uniform3f(loc("keyColor"), keyLight[0], keyLight[1], keyLight[2])
	g.uniform3f(loc("fillColor"), fillLight[0], fillLight[1], fillLight[2])
	dirs := lightDirs
	g.uniform3fv(loc("lightDir"), 3, unsafe.Pointer(&dirs[0]))
	modelLoc, skinLoc, unlitLoc := loc("model"), loc("skinEnabled"), loc("unlit")
	vsLoc, psLoc, blendLoc := loc("vertexShader"), loc("pixelShader"), loc("blendMode")
	texMatLoc, colorLoc, weightLoc := loc("textureMatrix"), loc("meshColor"), loc("textureWeight")
	effects := make([]model.Effects, len(sg.meshes))
	var opaque, transparent []drawItem
	for i, me := range sg.meshes {
		pose := frame.Poses[i]
		if len(pose) > 0 {
			g.activeTexture(0x84C1)
			g.bindTexture(0x0DE1, me.bones)
			g.texImage2D(0x0DE1, 0, 0x8814, 4, int32(len(pose)), 0, 0x1908, 0x1406, unsafe.Pointer(&pose[0][0]))
			runtime.KeepAlive(pose)
		}
		m := me.instance.Asset.Model
		effects[i] = m.Effects(ms)
		for j, b := range m.Batches {
			if !me.instance.Visible(b.Geoset) {
				continue
			}
			if b.Blend >= 2 && int(b.Blend) < len(blendFuncs) {
				transparent = append(transparent, drawItem{i, j, b.Priority})
			} else {
				opaque = append(opaque, drawItem{i, j, b.Priority})
			}
		}
	}
	// Transparent layers draw after opaque geometry, ordered by the batch's
	// priority plane; equal planes keep the model's layer order.
	sort.SliceStable(transparent, func(a, b int) bool { return transparent[a].priority < transparent[b].priority })
	g.frontFace(0x0901)
	g.cullFace(0x0405)
	current := -1
	for _, item := range append(opaque, transparent...) {
		me := sg.meshes[item.mesh]
		b := me.instance.Asset.Model.Batches[item.batch]
		units, pixel, vertex, ok := batchTextures(me, b, sg.white)
		if !ok {
			continue
		}
		if item.mesh != current {
			current = item.mesh
			mm := frame.Models[item.mesh]
			g.uniformMatrix4fv(modelLoc, 1, 0, unsafe.Pointer(&mm[0]))
			skin := int32(0)
			if len(frame.Poses[item.mesh]) > 0 {
				skin = 1
				g.activeTexture(0x84C1)
				g.bindTexture(0x0DE1, me.bones)
			}
			g.uniform1i(skinLoc, skin)
			g.bindVertexArray(me.vao)
		}
		if b.Blend >= 2 && int(b.Blend) < len(blendFuncs) {
			f := blendFuncs[b.Blend]
			g.enable(0x0BE2)
			g.blendFunc(f[0], f[1])
			g.depthMask(0)
		} else {
			g.disable(0x0BE2)
			g.depthMask(1)
		}
		if b.Flags&0x10 != 0 {
			g.depthMask(0)
		}
		if b.Flags&0x08 != 0 {
			g.disable(0x0B71)
		} else {
			g.enable(0x0B71)
		}
		if b.Flags&0x04 != 0 {
			g.disable(0x0B44)
		} else {
			g.enable(0x0B44)
		}
		fx := effects[item.mesh]
		weight := func(k int) float32 {
			if k < len(b.Weights) && b.Weights[k] >= 0 && b.Weights[k] < len(fx.Weights) {
				return fx.Weights[b.Weights[k]]
			}
			return 1
		}
		color := [4]float32{1, 1, 1, 1}
		if b.Color >= 0 && b.Color < len(fx.Colors) {
			color = fx.Colors[b.Color]
		}
		if b.TextureCount > 0 && b.BatchFlags&0x40 == 0 {
			color[3] *= weight(0)
		}
		var matrices [2]model.Mat4
		for k, t := range b.TexMatrix {
			matrices[k] = model.Identity()
			if t >= 0 && t < len(fx.Textures) {
				matrices[k] = fx.Textures[t]
			}
		}
		g.uniformMatrix4fv(texMatLoc, 2, 0, unsafe.Pointer(&matrices[0][0]))
		g.uniform4f(colorLoc, color[0], color[1], color[2], color[3])
		g.uniform3f(weightLoc, weight(0), weight(1), weight(2))
		g.uniform1i(vsLoc, int32(vertex))
		g.uniform1i(psLoc, int32(pixel))
		g.uniform1i(blendLoc, int32(b.Blend))
		unlit := int32(0)
		if b.Flags&0x01 != 0 {
			unlit = 1
		}
		g.uniform1i(unlitLoc, unlit)
		for k, unit := range []uint32{0x84C0, 0x84C2, 0x84C3, 0x84C4} {
			g.activeTexture(unit)
			g.bindTexture(0x0DE1, units[k])
		}
		g.drawElements(0x0004, int32(b.Count), 0x1405, uintptr(b.Start*4))
	}
	g.drawParticles(particles, sg, frame, ms, view, projection)
	g.activeTexture(0x84C0)
	g.disable(0x0B44)
	g.enable(0x0B71)
	g.disable(0x0BE2)
	g.depthMask(1)
	return nil
}

// drawParticles advances every emitter to ms and draws its quads after the
// transparent geometry: depth-tested, without depth writes or culling.
func (g gl) drawParticles(program uint32, sg *sceneGPU, frame model.Frame, ms uint32, view, projection model.Mat4) {
	g.useProgram(program)
	g.uniformMatrix4fv(g.getUniformLocation(program, "projection"), 1, 0, unsafe.Pointer(&projection[0]))
	g.uniform1i(g.getUniformLocation(program, "diffuse"), 0)
	blendLoc := g.getUniformLocation(program, "blendMode")
	g.bindVertexArray(sg.pvao)
	g.bindBuffer(0x8892, sg.pvbo)
	g.activeTexture(0x84C0)
	g.disable(0x0B44)
	g.enable(0x0B71)
	for i, ps := range sg.particles {
		if ps == nil {
			continue
		}
		m := sg.meshes[i].instance.Asset.Model
		emitters := make([]model.Mat4, len(m.Emitters))
		for k, e := range m.Emitters {
			emitters[k] = frame.Emitter(i, e)
		}
		ps.Update(ms, emitters)
		for _, b := range ps.Batches(view, emitters) {
			textures := sg.meshes[i].textures
			if b.Texture >= len(textures) || textures[b.Texture] == 0 {
				continue
			}
			if b.Blend >= 2 && int(b.Blend) < len(blendFuncs) {
				f := blendFuncs[b.Blend]
				g.enable(0x0BE2)
				g.blendFunc(f[0], f[1])
				g.depthMask(0)
			} else {
				g.disable(0x0BE2)
				g.depthMask(1)
			}
			g.uniform1i(blendLoc, int32(b.Blend))
			g.bindTexture(0x0DE1, textures[b.Texture])
			g.bufferData(0x8892, int64(len(b.Vertices)*4), unsafe.Pointer(&b.Vertices[0]), 0x88E0)
			g.drawArrays(0x0004, 0, int32(len(b.Vertices)/9))
			runtime.KeepAlive(b.Vertices)
		}
	}
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
