// Renders assets/tray_template.svg to assets/tray_template.png (88x88, RGBA).
// Run from the repo root: swift scripts/make_tray_icon.swift
import AppKit

let src = URL(fileURLWithPath: "assets/tray_template.svg")
let dst = URL(fileURLWithPath: "assets/tray_template.png")
guard let svg = NSImage(contentsOf: src) else { fatalError("cannot load \(src.path)") }
let size = 88
guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
                                 bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                 colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0) else { fatalError() }
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
NSColor.clear.set()
NSRect(x: 0, y: 0, width: size, height: size).fill()
svg.draw(in: NSRect(x: 0, y: 0, width: size, height: size))
NSGraphicsContext.restoreGraphicsState()
try! rep.representation(using: .png, properties: [:])!.write(to: dst)
print("wrote \(dst.path)")
