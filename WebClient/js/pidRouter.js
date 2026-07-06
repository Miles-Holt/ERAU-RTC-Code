// =============================================================================
// pidRouter.js — Turn-minimizing orthogonal pipe router.
//
// Pure functions. No DOM. No globals other than `PID`. Same source runs in the
// runtime diagram (pid.js) and the editor (editor.js) so both views draw the
// same SVG for the same YAML.
//
// Public API:
//   pidRoute(request) → { d, pts, error }
//   pidObstacleRects(objects) → Rect[]
//   pidRoundedPath(pts, r) → string
//   pidPipeSegs(routedPathsMap, excludeConnId) → { horiz, vert }
// =============================================================================

// ── Direction encoding — fixed order gives deterministic tie-breaks ─────────
const PID_DIR_N = 0, PID_DIR_E = 1, PID_DIR_S = 2, PID_DIR_W = 3;
const PID_DX = [ 0,  1,  0, -1];
const PID_DY = [-1,  0,  1,  0];
const PID_OPP = [PID_DIR_S, PID_DIR_W, PID_DIR_N, PID_DIR_E];
const PID_PORT_TO_DIR = { top: PID_DIR_N, right: PID_DIR_E, bottom: PID_DIR_S, left: PID_DIR_W };

// ── Router tuning ────────────────────────────────────────────────────────────
const PID_MAX_TURNS       = 5;
const PID_OVERLAP_PENALTY = 100;   // colinear pipe overlap: added to length, not turns

// ── Obstacle rectangles for components ───────────────────────────────────────
// Nodes are omitted (they're routing waypoints, not obstacles). All other
// component types are AABBs with PID.OBS_MARGIN padding. This is the only
// place in the router that knows about component types; when the component
// registry lands in a follow-up, this switch moves out.
function pidObstacleRects(objects) {
    const M = PID.OBS_MARGIN;
    const rects = [];
    for (const o of objects) {
        if (o.type === 'tank') {
            const rot = o.rotation || 0;
            const W = (o.gridW || 5) * PID.GRID;
            const H = (o.gridH || 8) * PID.GRID;
            const bW = (rot === 90 || rot === 270) ? H : W;
            const bH = (rot === 90 || rot === 270) ? W : H;
            const cx = o.gridX * PID.GRID + W / 2;
            const cy = o.gridY * PID.GRID + H / 2;
            rects.push({ x1: cx - bW/2 - M, y1: cy - bH/2 - M, x2: cx + bW/2 + M, y2: cy + bH/2 + M });
        } else if (o.type === 'graph') {
            rects.push({
                x1: o.gridX * PID.GRID - M,
                y1: o.gridY * PID.GRID - M,
                x2: o.gridX * PID.GRID + (o.gridW || 20) * PID.GRID + M,
                y2: o.gridY * PID.GRID + (o.gridH || 10) * PID.GRID + M,
            });
        } else if (o.type === 'valve') {
            const x = o.gridX * PID.GRID, y = o.gridY * PID.GRID, R = PID.VALVE_R;
            rects.push({ x1: x-R-M, y1: y-R-M, x2: x+R+M, y2: y+R+M });
        } else if (o.type === 'daqControl') {
            rects.push({
                x1: o.gridX * PID.GRID - M,
                y1: o.gridY * PID.GRID - M,
                x2: o.gridX * PID.GRID + (o.gridW || 10) * PID.GRID + M,
                y2: o.gridY * PID.GRID + (o.gridH || 3) * PID.GRID + M,
            });
        } else if (o.type === 'sensor') {
            rects.push({
                x1: o.gridX * PID.GRID - M,
                y1: o.gridY * PID.GRID - M,
                x2: o.gridX * PID.GRID + PID.SENSOR_W + M,
                y2: o.gridY * PID.GRID + PID.SENSOR_H + M,
            });
        }
    }
    return rects;
}

// ── SVG path builder with rounded corners ────────────────────────────────────
// Collapses runs of collinear points, then emits M/L/Q commands. Same shape as
// the old pid.js helper — kept identical so cached routes render byte-for-byte
// the same after refactor.
function pidRoundedPath(pts, r) {
    const s = [pts[0]];
    for (let i = 1; i < pts.length - 1; i++) {
        const prev = s[s.length - 1], curr = pts[i], next = pts[i + 1];
        const dx1 = Math.sign(curr.x - prev.x), dy1 = Math.sign(curr.y - prev.y);
        const dx2 = Math.sign(next.x - curr.x), dy2 = Math.sign(next.y - curr.y);
        if (dx1 !== dx2 || dy1 !== dy2) s.push(curr);
    }
    s.push(pts[pts.length - 1]);
    if (s.length < 2) return '';

    let d = 'M ' + s[0].x + ' ' + s[0].y;
    for (let i = 1; i < s.length; i++) {
        const prev = s[i - 1], curr = s[i], next = i < s.length - 1 ? s[i + 1] : null;
        if (next) {
            const dx1 = Math.sign(curr.x - prev.x), dy1 = Math.sign(curr.y - prev.y);
            const dx2 = Math.sign(next.x - curr.x), dy2 = Math.sign(next.y - curr.y);
            if (dx1 !== dx2 || dy1 !== dy2) {
                const len1 = Math.abs(curr.x - prev.x) + Math.abs(curr.y - prev.y);
                const len2 = Math.abs(next.x - curr.x) + Math.abs(next.y - curr.y);
                const rr   = Math.min(r, len1 / 2, len2 / 2);
                d += ' L ' + (curr.x - dx1 * rr) + ' ' + (curr.y - dy1 * rr);
                d += ' Q ' + curr.x + ' ' + curr.y + ' ' + (curr.x + dx2 * rr) + ' ' + (curr.y + dy2 * rr);
                continue;
            }
        }
        d += ' L ' + curr.x + ' ' + curr.y;
    }
    return d;
}

// ── Collect horiz/vert pipe segments from an ordered path map ────────────────
// Iteration order = insertion order (Map preserves it), which matches YAML
// order — that's what makes overlap avoidance deterministic across renders.
function pidPipeSegs(routedPathsMap, excludeConnId) {
    const horiz = [], vert = [];
    if (!routedPathsMap) return { horiz, vert };
    for (const [connId, pts] of routedPathsMap) {
        if (connId === excludeConnId) continue;
        for (let i = 0; i < pts.length - 1; i++) {
            const a = pts[i], b = pts[i+1];
            if (a.y === b.y && a.x !== b.x) {
                horiz.push({ y: a.y, x1: Math.min(a.x, b.x), x2: Math.max(a.x, b.x) });
            } else if (a.x === b.x && a.y !== b.y) {
                vert.push({ x: a.x, y1: Math.min(a.y, b.y), y2: Math.max(a.y, b.y) });
            }
        }
    }
    return { horiz, vert };
}

// ── Grid-cost model ──────────────────────────────────────────────────────────
// One bitmap for blocked cells; two bitmaps for edges that colinearly overlap
// existing pipes (traversing them costs OVERLAP_PENALTY extra). Perpendicular
// crossings cost nothing — pipes are allowed to cross at right angles.
function pidBuildGrid(rects, pipeSegs) {
    const G = PID.GRID;
    const W = Math.ceil(PID.CANVAS_W / G) + 1;
    const H = Math.ceil(PID.CANVAS_H / G) + 1;
    const blocked  = new Uint8Array(W * H);
    const hOverlap = new Uint8Array(W * H); // edge (gx,gy)–(gx+1,gy)
    const vOverlap = new Uint8Array(W * H); // edge (gx,gy)–(gx,gy+1)

    for (const r of rects) {
        const gx1 = Math.max(0, Math.floor(r.x1 / G));
        const gx2 = Math.min(W - 1, Math.ceil (r.x2 / G) - 1);
        const gy1 = Math.max(0, Math.floor(r.y1 / G));
        const gy2 = Math.min(H - 1, Math.ceil (r.y2 / G) - 1);
        for (let gy = gy1; gy <= gy2; gy++) {
            const row = gy * W;
            for (let gx = gx1; gx <= gx2; gx++) blocked[row + gx] = 1;
        }
    }

    if (pipeSegs) {
        for (const h of pipeSegs.horiz) {
            const gy = h.y / G;
            if (!Number.isInteger(gy) || gy < 0 || gy >= H) continue;
            const gx1 = h.x1 / G, gx2 = h.x2 / G;
            const lo = Math.max(0, Math.floor(gx1));
            const hi = Math.min(W - 1, Math.floor(gx2) - 1);
            const row = gy * W;
            for (let gx = lo; gx <= hi; gx++) hOverlap[row + gx] = 1;
        }
        for (const v of pipeSegs.vert) {
            const gx = v.x / G;
            if (!Number.isInteger(gx) || gx < 0 || gx >= W) continue;
            const gy1 = v.y1 / G, gy2 = v.y2 / G;
            const lo = Math.max(0, Math.floor(gy1));
            const hi = Math.min(H - 1, Math.floor(gy2) - 1);
            for (let gy = lo; gy <= hi; gy++) vOverlap[gy * W + gx] = 1;
        }
    }

    return { W, H, blocked, hOverlap, vOverlap };
}

// ── Priority queue (binary min-heap over lexicographic (turns, cost, dir, gy, gx)) ─
function pidMakeHeap() {
    const a = [];
    // Lex compare: turns → cost → dir → gy → gx. All tie-breaks are deterministic.
    function lt(x, y) {
        if (x.turns !== y.turns) return x.turns < y.turns;
        if (x.cost  !== y.cost ) return x.cost  < y.cost;
        if (x.dir   !== y.dir  ) return x.dir   < y.dir;
        if (x.gy    !== y.gy   ) return x.gy    < y.gy;
        return x.gx < y.gx;
    }
    return {
        push(v) {
            a.push(v);
            let i = a.length - 1;
            while (i > 0) {
                const p = (i - 1) >> 1;
                if (lt(a[i], a[p])) { const t = a[i]; a[i] = a[p]; a[p] = t; i = p; }
                else break;
            }
        },
        pop() {
            if (a.length === 0) return null;
            const top = a[0];
            const last = a.pop();
            if (a.length > 0) {
                a[0] = last;
                let i = 0;
                for (;;) {
                    const l = 2 * i + 1, r = 2 * i + 2;
                    let m = i;
                    if (l < a.length && lt(a[l], a[m])) m = l;
                    if (r < a.length && lt(a[r], a[m])) m = r;
                    if (m === i) break;
                    const t = a[i]; a[i] = a[m]; a[m] = t; i = m;
                }
            }
            return top;
        },
        size: () => a.length,
    };
}

// ── Dijkstra over (gx, gy, dir), lex-cost (turns, length + overlap penalty) ──
// Primary key is turns, so the first goal state popped is guaranteed to be a
// fewest-turns route. Ties break by shortest length (with overlap penalized).
function pidSearch(startGx, startGy, startDir, goalGx, goalGy, goalDir, grid) {
    const { W, H, blocked, hOverlap, vOverlap } = grid;
    const N = W * H * 4;
    const bestTurns = new Uint8Array(N);   // 0 = unvisited (we set +1 sentinel below)
    const bestCost  = new Float64Array(N); // 0 = unvisited
    const parent    = new Int32Array(N);   // -1 = none
    for (let i = 0; i < N; i++) parent[i] = -1;

    function keyOf(gx, gy, dir) { return (gy * W + gx) * 4 + dir; }

    const startKey = keyOf(startGx, startGy, startDir);
    bestTurns[startKey] = 1;              // real turns = stored - 1 (sentinel)
    bestCost[startKey]  = 1;              // real cost  = stored - 1 (sentinel)

    const heap = pidMakeHeap();
    heap.push({ gx: startGx, gy: startGy, dir: startDir, turns: 0, cost: 0 });

    while (heap.size() > 0) {
        const s = heap.pop();

        // Skip stale heap entries — a better path to this state was found later.
        const sk = keyOf(s.gx, s.gy, s.dir);
        if (bestTurns[sk] - 1 !== s.turns || bestCost[sk] - 1 !== s.cost) continue;

        if (s.gx === goalGx && s.gy === goalGy && s.dir === goalDir) {
            // Reconstruct: chase parent pointers, encoding dir at each step so
            // the collapser can drop collinear passes.
            const states = [];
            let k = sk;
            while (k !== -1) {
                const gy = Math.floor(k / (W * 4));
                const rest = k - gy * W * 4;
                const gx = Math.floor(rest / 4);
                const dir = rest - gx * 4;
                states.push({ gx, gy, dir });
                k = parent[k];
            }
            states.reverse();
            return states;
        }

        // 3 successors: straight (no turn), or turn to either perpendicular dir.
        // Enumeration order: straight → clockwise → counterclockwise. Fixed
        // order + heap tie-breaks = deterministic output.
        const trySucc = (nd, isTurn) => {
            const ngx = s.gx + PID_DX[nd];
            const ngy = s.gy + PID_DY[nd];
            if (ngx < 0 || ngx >= W || ngy < 0 || ngy >= H) return;
            const cellIdx = ngy * W + ngx;
            if (blocked[cellIdx] && !(ngx === goalGx && ngy === goalGy)) return;

            // Edge overlap check: which precomputed bitmap holds this edge?
            let stepCost = 1;
            if (nd === PID_DIR_E) {
                if (hOverlap[s.gy * W + s.gx]) stepCost += PID_OVERLAP_PENALTY;
            } else if (nd === PID_DIR_W) {
                if (hOverlap[s.gy * W + ngx]) stepCost += PID_OVERLAP_PENALTY;
            } else if (nd === PID_DIR_S) {
                if (vOverlap[s.gy * W + s.gx]) stepCost += PID_OVERLAP_PENALTY;
            } else /* N */ {
                if (vOverlap[ngy * W + s.gx]) stepCost += PID_OVERLAP_PENALTY;
            }

            const nTurns = s.turns + (isTurn ? 1 : 0);
            if (nTurns > PID_MAX_TURNS) return;
            const nCost = s.cost + stepCost;

            const nk = keyOf(ngx, ngy, nd);
            const bt = bestTurns[nk] - 1, bc = bestCost[nk] - 1;
            if (bestTurns[nk] === 0 || nTurns < bt || (nTurns === bt && nCost < bc)) {
                bestTurns[nk] = nTurns + 1;
                bestCost[nk]  = nCost  + 1;
                parent[nk]    = sk;
                heap.push({ gx: ngx, gy: ngy, dir: nd, turns: nTurns, cost: nCost });
            }
        };

        trySucc(s.dir, false);
        trySucc((s.dir + 1) & 3, true);   // clockwise
        trySucc((s.dir + 3) & 3, true);   // counterclockwise
    }

    return null;
}

// ── Grid-snap helper ─────────────────────────────────────────────────────────
// Ports and stub tips should already be on grid multiples, but round defensively.
function pidGridSnap(v) { return Math.round(v / PID.GRID); }

// ── Direct straight-pipe fast path ───────────────────────────────────────────
// When two ports are colinear and face each other, a single straight segment is
// the ideal pipe — no standoff, no loop-back. The stub-based search can't
// produce this once the ports are closer than 2*STUB apart (the projected stub
// tips overshoot and cross, forcing extra turns), so we detect it up front.
function pidRectContains(r, p) {
    return p.x >= r.x1 && p.x <= r.x2 && p.y >= r.y1 && p.y <= r.y2;
}
function pidSegBlocked(a, b, rects) {
    const xlo = Math.min(a.x, b.x), xhi = Math.max(a.x, b.x);
    const ylo = Math.min(a.y, b.y), yhi = Math.max(a.y, b.y);
    for (const r of rects) {
        // Skip each endpoint's own component rect — the port sits on its
        // boundary (inside the clearance margin), so it always "intersects".
        if (pidRectContains(r, a) || pidRectContains(r, b)) continue;
        if (xhi >= r.x1 && xlo <= r.x2 && yhi >= r.y1 && ylo <= r.y2) return true;
    }
    return false;
}
function pidTryStraight(p1, d1, p2, d2, rects) {
    const dir1 = PID_PORT_TO_DIR[d1];
    if (PID_PORT_TO_DIR[d2] !== PID_OPP[dir1]) return null;   // must face each other
    if (dir1 === PID_DIR_E || dir1 === PID_DIR_W) {
        if (p1.y !== p2.y) return null;                       // colinear horizontally
        if (Math.sign(p2.x - p1.x) !== PID_DX[dir1]) return null; // p2 in front of p1
    } else {
        if (p1.x !== p2.x) return null;                       // colinear vertically
        if (Math.sign(p2.y - p1.y) !== PID_DY[dir1]) return null;
    }
    if (pidSegBlocked(p1, p2, rects)) return null;
    return [{ x: p1.x, y: p1.y }, { x: p2.x, y: p2.y }];
}

// ── Public route entry point ─────────────────────────────────────────────────
// request = { p1, d1, p2, d2, objects, pipeSegs }
//   p1, p2: {x, y} pixel coords of the two ports (from portPos)
//   d1, d2: 'top' | 'right' | 'bottom' | 'left' — port outward direction
//   objects: full layout objects[] (for obstacles)
//   pipeSegs: {horiz, vert} from pidPipeSegs, or null to ignore existing pipes
// Returns: { d: svgPathString, pts: routeWaypoints[], error: nullOrString }
function pidRoute(request) {
    const { p1, d1, p2, d2, objects, pipeSegs } = request;
    const G = PID.GRID, S = PID.STUB, R = PID.CORNER_R;

    const dirIdx1 = PID_PORT_TO_DIR[d1];
    const dirIdx2 = PID_PORT_TO_DIR[d2];
    const s1 = { x: p1.x + PID_DX[dirIdx1] * S, y: p1.y + PID_DY[dirIdx1] * S };
    const s2 = { x: p2.x + PID_DX[dirIdx2] * S, y: p2.y + PID_DY[dirIdx2] * S };

    const rects = pidObstacleRects(objects);

    // Prefer a direct straight pipe when the ports face each other colinearly
    // with a clear path — avoids the forced double standoff / loop-back.
    const straightPts = pidTryStraight(p1, d1, p2, d2, rects);
    if (straightPts) return { d: pidRoundedPath(straightPts, R), pts: straightPts, error: null };

    const startGx = pidGridSnap(s1.x), startGy = pidGridSnap(s1.y);
    const goalGx  = pidGridSnap(s2.x), goalGy  = pidGridSnap(s2.y);
    const startDir = dirIdx1;
    const goalDir  = PID_OPP[dirIdx2];   // arrive at s2 facing INTO p2

    // Three fallback passes: strict (obstacles + pipe overlap penalty), relaxed
    // (no pipe penalty), open (no obstacles). Each pass is a deterministic
    // Dijkstra; the layering handles the case where no path fits within the
    // turn cap under stricter constraints.
    let states = null;
    let error  = null;

    const gridStrict = pidBuildGrid(rects, pipeSegs);
    states = pidSearch(startGx, startGy, startDir, goalGx, goalGy, goalDir, gridStrict);

    if (!states) {
        const gridRelaxed = pidBuildGrid(rects, null);
        states = pidSearch(startGx, startGy, startDir, goalGx, goalGy, goalDir, gridRelaxed);
    }
    if (!states) {
        const gridOpen = pidBuildGrid([], null);
        states = pidSearch(startGx, startGy, startDir, goalGx, goalGy, goalDir, gridOpen);
        if (states) error = 'Could not route without crossing an object';
    }
    if (!states) {
        error = 'Could not route within ' + PID_MAX_TURNS + ' turns';
        const pts = [p1, s1, s2, p2];
        return { d: pidRoundedPath(pts, R), pts, error };
    }

    // Compress: keep first, last, and corners (where dir changes on the next step).
    // pidRoundedPath will re-collapse collinear points defensively.
    const pts = [{ x: p1.x, y: p1.y }];
    for (let i = 0; i < states.length; i++) {
        const isCorner = i === states.length - 1 || states[i + 1].dir !== states[i].dir;
        if (isCorner || i === 0) {
            pts.push({ x: states[i].gx * G, y: states[i].gy * G });
        }
    }
    pts.push({ x: p2.x, y: p2.y });

    return { d: pidRoundedPath(pts, R), pts, error };
}
