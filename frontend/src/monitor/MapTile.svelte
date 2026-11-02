<script>
  import { _ } from 'svelte-i18n';
  import { oidName } from '../utils/oidDisplay';
  import { latestFor, stateText, stateKind } from '../utils/stateColour';
  import { ReadMapAsset } from '../bridge/App';

  /**
   * A drawing whose parts are readings.
   *
   * Everything drawn here comes from a FROZEN VOCABULARY the Go side owns and
   * validated before it arrived: four shape types, coordinates that are
   * percentages between 0 and 100, and a background that is a file name the
   * operator's own directory resolved. Nothing a preset wrote becomes markup.
   * See pkg/preset/map.go, and in particular why this is not SVG the preset
   * supplies.
   *
   * The lines ARE an <svg>, and that is not a contradiction: this component
   * writes it, from numbers Go has already bounded. A line is the one shape
   * absolute positioning cannot draw without a rotation whose length is
   * trigonometry — the svg does it with two coordinates.
   *
   * Props only, no store: what keeps it clean under reactive.test.mjs.
   */
  export let drawing;
  export let labels = {};
  export let points = [];
  export let targets = [];
  export let mibTree = null;

  // The background, fetched once per name. It crosses the bridge as a data URI
  // because the renderer has no filesystem — see App.ReadMapAsset.
  let background = '';
  let backgroundError = '';
  let loadedName = '';

  $: loadBackground(drawing?.background || '');

  async function loadBackground(name) {
    if (name === loadedName) return;
    loadedName = name;
    background = '';
    backgroundError = '';
    if (!name) return;
    try {
      background = await ReadMapAsset(name);
    } catch (err) {
      // Said, not swallowed. A map whose background is missing draws its shapes
      // on nothing, which is a legitimate drawing and an alarming surprise —
      // the operator has to know it is the picture that is absent rather than
      // the readings.
      backgroundError = String(err?.message || err || name);
    }
  }

  // Every function called from the markup takes what it reads as an argument.

  /**
   * The points that decide this drawing's colours.
   *
   * On a group of equipments a map shows the FIRST one and says so. A drawing
   * is a picture of one rack: overlaying two switches' readings on one set of
   * shapes would colour each box by whichever answered last, which is the
   * defect the wall of cells was fixed for — and unlike the wall, a map cannot
   * be split, because its shapes are placed by hand.
   */
  const forFirst = (all, list) => {
    if (!Array.isArray(list) || list.length < 2) return all || [];
    return (all || []).filter((p) => p.target === list[0]);
  };

  const shapeStyle = (s) => {
    const n = (v, fallback) => (Number.isFinite(Number(v)) ? Number(v) : fallback);
    const left = `left:${n(s.x, 0)}%;top:${n(s.y, 0)}%`;
    if (s.type === 'rect') return `${left};width:${n(s.w, 1)}%;height:${n(s.h, 1)}%`;
    return left;
  };

  const titleOf = (s, tree) => {
    if (!s.oid) return s.text || '';
    const name = oidName(s.oid, tree);
    return name && name !== s.oid ? `${name} — ${s.oid}` : s.oid;
  };

  // A shape with no reading is decoration — the outline of the rack, the name
  // of a room — and is drawn in the neutral class rather than as "no data",
  // which would make every label on the map look like a failed poll.
  const kindOf = (s, all, map) =>
    s.oid ? stateKind(latestFor(s.oid, all), map) : 'plain';

  const textOf = (s, all, map) => {
    if (!s.oid) return s.text || '';
    const value = stateText(latestFor(s.oid, all), map);
    return s.text ? `${s.text} ${value}` : value;
  };

  // The drawing's own proportions, bounded here as well as in Go: this is a
  // CSS value and an aspect-ratio of 0 collapses the widget to nothing.
  const aspectOf = (d) => {
    const v = Number(d && d.aspect);
    return Number.isFinite(v) && v >= 0.2 && v <= 12 ? v : 16 / 9;
  };

  const lines = (shapes) => (shapes || []).filter((s) => s.type === 'line');
  const solids = (shapes) => (shapes || []).filter((s) => s.type !== 'line');
</script>

<div class="map" style="--aspect:{aspectOf(drawing)}">
  {#if background}
    <img class="bg" src={background} alt="" />
  {:else if backgroundError}
    <p class="bg-missing">{$_('dashboard.mapBackgroundMissing', { values: { name: drawing.background } })}</p>
  {/if}

  <!-- The overlay carries the drawing. `preserveAspectRatio="none"` is what
       makes a 0..100 viewBox mean percentages of the box in both axes
       independently, which is the same coordinate system the boxes above use. -->
  <svg class="links" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
    {#each lines(drawing.shapes) as s, i (i)}
      <!-- `class="link …"` with a STATIC part, and that is not style: a class
           attribute that is only an expression REPLACES the scoping class
           Svelte adds, so the compiled selector — `line:where(.svelte-xxx)` —
           matched nothing and every link was drawn with no stroke at all. It
           was invisible in the first capture and green in every test. -->
      <line
        x1={s.x} y1={s.y} x2={s.x2} y2={s.y2}
        class="link {kindOf(s, forFirst(points, targets), labels)}"
      />
    {/each}
  </svg>

  {#each solids(drawing.shapes) as s, i (i)}
    <div
      class="shape {s.type} {kindOf(s, forFirst(points, targets), labels)}"
      style={shapeStyle(s)}
      title={titleOf(s, mibTree)}
    >
      {#if s.type !== 'dot'}
        <span class="shape-text">{textOf(s, forFirst(points, targets), labels)}</span>
      {/if}
    </div>
  {/each}

  {#if targets.length > 1}
    <!-- A map is a picture of ONE rack, and this dashboard is showing several
         equipments. Said on the drawing rather than left to be inferred from
         the readings, which all look perfectly plausible. -->
    <p class="one-of">{$_('dashboard.mapFirstEquipment', { values: { target: targets[0] } })}</p>
  {/if}
</div>

<style>
  .map {
    position: relative;
    width: 100%;
    /* A definite height, because every coordinate here is a percentage of it —
       and the drawing says what shape it wants, because a rack front panel is
       about eight to one and a site plan is about four to three. A background
       of another shape is letterboxed rather than stretched. */
    aspect-ratio: var(--aspect, 1.778);
    min-height: 160px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background-color: var(--bg-light-color);
    overflow: hidden;
  }

  .bg {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    object-fit: contain;
    opacity: 0.9;
  }

  .bg-missing {
    position: absolute;
    inset: auto 0 0 0;
    margin: 0;
    padding: 0.25rem 0.4rem;
    color: var(--text-muted);
    font-size: 0.72rem;
    text-align: center;
  }

  .links {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
  }

  .links line {
    stroke: var(--text-muted);
    stroke-width: 2;
    vector-effect: non-scaling-stroke;
  }

  .links line.good { stroke: var(--success-color, #3fb950); }
  .links line.bad { stroke: var(--error-color, #f85149); }
  .links line.warn { stroke: var(--warning-color, #d29922); }
  .links line.failed { stroke: var(--error-color, #f85149); }

  .shape {
    position: absolute;
    display: flex;
    align-items: center;
    justify-content: center;
    min-width: 0;
    font-size: 0.7rem;
    line-height: 1.1;
  }

  .shape-text {
    padding: 0 2px;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* A box carries its state on its border and a wash of it inside, so a rack
     of forty boxes reads as a shape rather than as a block of colour. */
  .shape.rect {
    border: 1px solid var(--border-color);
    border-radius: 2px;
    background-color: var(--bg-color);
    transform: translate(0, 0);
  }

  .shape.rect.good { border-color: var(--success-color, #3fb950); }
  .shape.rect.bad {
    border-color: var(--error-color, #f85149);
    background-color: var(--error-subtle, var(--bg-color));
  }
  .shape.rect.warn { border-color: var(--warning-color, #d29922); }
  .shape.rect.failed { border-color: var(--error-color, #f85149); }
  .shape.rect.unknown { border-style: dashed; }

  .shape.dot {
    width: 10px;
    height: 10px;
    margin: -5px 0 0 -5px;
    border-radius: 50%;
    background-color: var(--text-muted);
  }

  .shape.dot.good { background-color: var(--success-color, #3fb950); }
  .shape.dot.bad { background-color: var(--error-color, #f85149); }
  .shape.dot.warn { background-color: var(--warning-color, #d29922); }
  .shape.dot.failed { background-color: var(--error-color, #f85149); }

  .shape.label {
    transform: translate(-50%, -50%);
    color: var(--text-color);
    font-weight: 600;
    white-space: nowrap;
  }

  .one-of {
    position: absolute;
    inset: 0 0 auto auto;
    margin: 0;
    padding: 0.15rem 0.35rem;
    color: var(--text-muted);
    font-size: 0.68rem;
  }
</style>
