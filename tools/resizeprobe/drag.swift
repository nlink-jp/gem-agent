// drag.swift — narrow or widen ONE window by dragging its right edge with
// synthesized mouse events, the way a person does (ADR-0094).
//
// resizeprobe -auto -drag runs it with `swift`. A resize through the
// terminal's own interface (kitty's remote control, iTerm2's AppleScript)
// changes the width in one atomic step; a mouse drag makes the terminal
// reflow continuously while it reports only now and then, and by hand the
// erase arm left stale rows that no atomic resize reproduced.
//
// Synthesized mouse events land on whatever is under the pointer, so this
// refuses to press unless the target window is frontmost at the starting
// point, restores the pointer afterwards, and never retries.
//
// usage: swift drag.swift <window number> <fraction of the current width> <milliseconds>
// exit: 0 dragged (prints the width before and after, in points),
//       2 not trusted for Accessibility, 3 the window is not under the pointer,
//       4 the window was not found, 5 bad arguments.

import AppKit
import CoreGraphics
import Foundation

func fail(_ code: Int32, _ msg: String) -> Never {
    FileHandle.standardError.write((msg + "\n").data(using: .utf8)!)
    exit(code)
}

let args = CommandLine.arguments
guard args.count == 4, let wid = Int(args[1]), let fraction = Double(args[2]),
      let millis = Double(args[3]), fraction > 0.2, fraction < 3 else {
    fail(5, "usage: drag.swift <window number> <fraction 0.2..3> <milliseconds>")
}
guard AXIsProcessTrusted() else {
    fail(2, "not trusted for Accessibility: allow the app running this in System Settings > Privacy & Security > Accessibility")
}

struct Win { let number: Int; let pid: pid_t; let bounds: CGRect; let layer: Int }

func windows(_ opts: CGWindowListOption, _ rel: CGWindowID = kCGNullWindowID) -> [Win] {
    let list = CGWindowListCopyWindowInfo(opts, rel) as? [[String: Any]] ?? []
    return list.compactMap { d in
        guard let n = d[kCGWindowNumber as String] as? Int,
              let pid = d[kCGWindowOwnerPID as String] as? Int,
              let b = d[kCGWindowBounds as String] as? [String: Double],
              let layer = d[kCGWindowLayer as String] as? Int else { return nil }
        return Win(number: n, pid: pid_t(pid), bounds: CGRect(x: b["X"] ?? 0, y: b["Y"] ?? 0,
                   width: b["Width"] ?? 0, height: b["Height"] ?? 0), layer: layer)
    }
}

func find() -> Win? { windows(.optionIncludingWindow, CGWindowID(wid)).first }

guard let target = find() else { fail(4, "window \(wid) not found") }
NSRunningApplication(processIdentifier: target.pid)?.activate()
Thread.sleep(forTimeInterval: 0.8)

// The right edge, mid-height, one point inside the window.
let start = CGPoint(x: target.bounds.maxX - 1, y: target.bounds.midY)
// Front to back; layers 0..<20 are ordinary and floating windows (the Dock
// keeps a transparent full-screen window at 20).
let top = windows([.optionOnScreenOnly]).first { $0.layer >= 0 && $0.layer < 20 && $0.bounds.contains(start) }
guard top?.number == wid else {
    fail(3, "window \(wid) is not the topmost window at the drag point (topmost: \(top.map { String($0.number) } ?? "none")); nothing pressed")
}

let saved = CGEvent(source: nil)?.location ?? start
let dx = target.bounds.width * (fraction - 1)
let steps = max(10, Int(millis / 16))
func post(_ type: CGEventType, _ p: CGPoint) {
    CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: p, mouseButton: .left)?
        .post(tap: .cghidEventTap)
}
post(.mouseMoved, start)
Thread.sleep(forTimeInterval: 0.1)
post(.leftMouseDown, start)
for i in 1...steps {
    Thread.sleep(forTimeInterval: millis / 1000 / Double(steps))
    post(.leftMouseDragged, CGPoint(x: start.x + dx * Double(i) / Double(steps), y: start.y))
}
post(.leftMouseUp, CGPoint(x: start.x + dx, y: start.y))
Thread.sleep(forTimeInterval: 0.3)
post(.mouseMoved, saved)

let after = find()?.bounds.width ?? -1
print("width \(Int(target.bounds.width)) -> \(Int(after)) points")
