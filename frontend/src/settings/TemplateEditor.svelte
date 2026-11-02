<script>
  import { onMount, tick } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import {
    NotifyTemplateVariables,
    NotifyPreviewTemplate,
    NotifyDefaultBody,
  } from '../bridge/App';

  /**
   * The sink being edited, bound two-way.
   *
   * The whole sink rather than the template alone: the preview has to know the
   * kind and the payload mode to show what will actually be sent, and passing
   * those as separate props is how they came to disagree with each other.
   */
  export let sink;

  /**
   * The format the BODY is written in, or '' when the body is prose.
   *
   * This used to be a single boolean meaning "webhook in template mode", which
   * was the same thing as "the body is JSON" only because JSON was the only
   * format there was. It now names the format, because the escaping, the
   * Content-Type and the starting default all follow it.
   */
  $: bodyFormat =
    sink.kind === 'webhook' && sink.webhook?.payloadMode === 'template'
      ? (sink.webhook.bodyFormat || 'json')
      : sink.kind === 'email' && sink.email?.format === 'html'
        ? 'html'
        : '';

  /** Formats that ship a usable starting point, offered as a button. */
  $: hasDefaultBody = bodyFormat === 'json' || bodyFormat === 'html';
  // Whether this sink masks addresses, so the variable list can say which
  // values will be redacted. It was left behind as a bare `redact` when this
  // component moved from four props to the whole sink: Svelte compiled it to a
  // reference to nothing and warned, the build passed, and the two branches
  // below threw ReferenceError the moment the variable list was opened.
  $: redact = !!sink.redact;

  // The vocabulary comes from Go rather than being mirrored here: a hand-copied
  // list is a list that drifts, and the first symptom would be a variable the
  // editor offers and the renderer does not know.
  let variables = [];
  let preview = null;
  let previewKind = 'threshold';
  let showVariables = false;
  let subjectEl;
  let bodyEl;
  let lastFocused = 'body';

  onMount(async () => {
    try {
      variables = (await NotifyTemplateVariables()) || [];
    } catch (e) {
      variables = [];
    }
    refreshPreview();
  });

  // A preview is not decoration. buildMessage silently substitutes the event
  // summary for an empty subject, so a template that renders to nothing looks
  // exactly like one that works — the only way to tell them apart is to look.
  let previewTimer;
  function refreshPreview() {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(async () => {
      try {
        preview = await NotifyPreviewTemplate(sink, previewKind);
      } catch (e) {
        preview = null;
      }
    }, 200);
  }

  // `sink` is watched whole: the preview depends on the template, the kind, the
  // payload mode and the redaction flag, and naming them one by one is how one
  // of them gets forgotten.
  $: sink, previewKind, refreshPreview();

  // What the preview is showing, from the ANSWER rather than guessed alongside
  // it. A webhook posts an object in both modes, a syslog sink sends one line
  // whatever the template says, and a mail sink sends headers — none of which
  // the editor can work out from the fields in front of it without repeating a
  // decision that lives in Go.
  $: wire = preview?.format || 'text';
  $: asPayload = wire !== 'text';

  // Whether this format HAS a notion of being well-formed.
  //
  // JSON and XML are parsed before the request goes out; a syslog line, a mail
  // message, plain text and a form body are not, because there is nothing there
  // to be malformed. Keying the badge off "not plain text" instead — which is
  // what this did — announced "valid JSON · 277 bytes" over a syslog line.
  $: checksShape = wire === 'json' || wire === 'xml';
  $: validLabel = wire === 'xml' ? 'notify.xmlValid' : 'notify.jsonValid';
  $: invalidLabel = wire === 'xml' ? 'notify.xmlInvalid' : 'notify.jsonInvalid';

  // The rendered subject, when the transport actually carries one separately.
  // An email's subject is inside the headers below, and a syslog line has no
  // subject at all — showing one there would suggest it is sent.
  $: showSubject = wire === 'text';

  const WIRE_LABEL = {
    syslog: 'notify.previewSyslog',
    email: 'notify.previewEmail',
    json: 'notify.previewPayload',
    xml: 'notify.previewPayload',
    form: 'notify.previewPayload',
    text: 'notify.templatePreview',
  };

  async function insert(name) {
    const token = '{{' + name + '}}';
    const el = lastFocused === 'subject' ? subjectEl : bodyEl;
    const field = lastFocused === 'subject' ? 'subject' : 'body';
    const current = sink.template[field] || '';

    if (!el) {
      sink.template[field] = current + token;
      return;
    }
    const start = el.selectionStart ?? current.length;
    const end = el.selectionEnd ?? current.length;
    sink.template[field] = current.slice(0, start) + token + current.slice(end);

    await tick();
    el.focus();
    el.setSelectionRange(start + token.length, start + token.length);
  }

  // Starting from something that already works beats starting from a blank
  // field that will not save.
  async function useDefaultPayload() {
    try {
      const body = await NotifyDefaultBody(bodyFormat);
      if (body) sink.template.body = body;
    } catch (e) {
      /* leave the body alone */
    }
  }

  // `p` is a parameter, not a read of `preview`: the preview is fetched and
  // replaced as you type, and an `{#each}` that does not name it goes on showing
  // the errors of a template two edits ago.
  function errorsFor(field, p) {
    return (p?.errors || []).filter((e) => e.field === field);
  }
</script>

<div class="tpl">
  <div class="tpl-head">
    <h5>{$_('notify.templateTitle')}</h5>
    {#if hasDefaultBody}
      <button class="btn btn-small" on:click={useDefaultPayload}>
        {$_('notify.useDefaultPayload')}
      </button>
    {/if}
    <button class="btn btn-small" on:click={() => (showVariables = !showVariables)}>
      <Icon name={showVariables ? 'chevron-up' : 'chevron-down'} size={13} />
      {$_('notify.insertVariable')}
    </button>
  </div>
  <p class="hint">{$_('notify.templateHint')}</p>

  <div class="tpl-main">
    <div class="tpl-fields">
  {#if showVariables}
    <div class="vars">
      {#each variables as v (v.name)}
        <button class="var" on:click={() => insert(v.name)} title={$_(v.description)}>
          <code>{'{{' + v.name + '}}'}</code>
          <span class="ex">{v.example}</span>
          {#if redact && v.redacted}
            <span class="masked">{$_('notify.tplMasked')}</span>
          {/if}
        </button>
      {/each}
      <p class="note">{$_('notify.tplParamsHint')}</p>
      {#if redact}
        <p class="note warn">
          <Icon name="triangle-alert" size={13} /> {$_('notify.tplRedactNote')}
        </p>
      {/if}
    </div>
  {/if}

  <label class="fld">
    <span>{$_('notify.templateSubject')}</span>
    <input type="text" bind:this={subjectEl} bind:value={sink.template.subject}
      on:focus={() => (lastFocused = 'subject')}
      placeholder={$_('notify.templateDefault')} />
    {#each errorsFor('subject', preview) as err}
      <span class="err"><Icon name="circle-x" size={12} /> {err.message}</span>
    {/each}
  </label>

  <label class="fld">
    <span>{$_('notify.templateBody')}</span>
    <textarea rows="7" bind:this={bodyEl} bind:value={sink.template.body}
      on:focus={() => (lastFocused = 'body')}
      placeholder={$_('notify.templateDefault')}></textarea>
    {#each errorsFor('body', preview) as err}
      <span class="err"><Icon name="circle-x" size={12} /> {err.message}</span>
    {/each}
  </label>

    </div>

  <div class="preview">
    <div class="preview-head">
      <span>{$_(WIRE_LABEL[wire] || 'notify.templatePreview')}</span>
      {#if preview?.contentType}
        <code class="pv-ctype" title={$_('notify.previewContentType')}>{preview.contentType}</code>
      {/if}
      {#if preview && checksShape}
        {#if preview.jsonError}
          <span class="pv-bad" title={preview.jsonError}>{$_(invalidLabel)}</span>
        {:else}
          <span class="pv-ok">{$_(validLabel, { values: { bytes: preview.bytes } })}</span>
        {/if}
      {:else if preview && asPayload}
        <!-- No shape to be wrong: the size is still worth knowing, because a
             syslog line over the collector's limit is silently truncated and a
             mail body is not. -->
        <span class="pv-size">{$_('notify.previewBytes', { values: { bytes: preview.bytes } })}</span>
      {/if}
      <select bind:value={previewKind}>
        <option value="threshold">{$_('events.category.threshold')}</option>
        <option value="trap">{$_('events.category.trap')}</option>
        <option value="reachability">{$_('events.category.reachability')}</option>
      </select>
    </div>
    {#if preview}
      <div class="preview-body">
        {#if showSubject}
          <div class="pv-subject">{preview.subject}</div>
        {/if}
        <pre class="pv-text" class:pv-json={asPayload}>{preview.body}</pre>
        {#if wire === 'syslog' && sink.template?.subject}
          <p class="pv-note">{$_('notify.previewSyslogNoSubject')}</p>
        {/if}
        {#if preview.jsonError}
          <p class="pv-error">{preview.jsonError}</p>
        {/if}
      </div>
    {/if}
  </div>
  </div>

  {#if bodyFormat && bodyFormat !== 'text'}
    <p class="note warn">
      <Icon name="triangle-alert" size={13} /> {$_('notify.tplJsonNote')}
    </p>
  {/if}
  <p class="note">{$_('notify.tplQueuedNote')}</p>
  <p class="note">{$_('notify.tplSharedNote')}</p>
</div>

<style>
  .tpl {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--border-color);
  }

  /* One column by default. NotifySettings decides when this splits, so the
     breakpoint has a single home rather than one per component. */
  .tpl-main {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }

  .tpl-fields {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }

  .tpl-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }

  h5 {
    margin: 0;
    font-size: 0.85em;
    color: var(--text-color);
  }

  .hint,
  .note {
    margin: 0;
    font-size: 0.75em;
    color: var(--text-muted);
    display: flex;
    align-items: center;
    gap: 5px;
  }

  .note.warn {
    color: var(--warning-color);
  }

  .vars {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
    padding: 8px;
    background-color: var(--bg-lighter-color);
    border-radius: 4px;
  }

  .var {
    display: flex;
    align-items: baseline;
    gap: 5px;
    padding: 3px 7px;
    background: none;
    border: 1px solid var(--border-color);
    border-radius: 3px;
    cursor: pointer;
    font-size: 0.75em;
    color: var(--text-color);
  }

  .var:hover {
    border-color: var(--accent-color);
  }

  .var code {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }

  .ex {
    color: var(--text-muted);
    max-width: 130px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .masked {
    color: var(--warning-color);
  }

  .fld {
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-size: 0.8em;
    color: var(--text-muted);
  }

  .fld input,
  .fld textarea {
    width: 100%;
    padding: 6px 8px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.95em;
  }

  .fld textarea {
    resize: vertical;
    white-space: pre;
    overflow-x: auto;
  }

  .err {
    color: var(--error-color);
    display: flex;
    align-items: center;
    gap: 4px;
  }

  .preview {
    border: 1px solid var(--border-color);
    border-radius: 4px;
    overflow: hidden;
  }

  .preview-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 5px 8px;
    background-color: var(--bg-lighter-color);
    font-size: 0.75em;
    color: var(--text-muted);
  }

  .preview-head select {
    background-color: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 3px;
    color: var(--text-color);
    font-size: 1em;
    padding: 2px 4px;
  }

  .preview-body {
    padding: 8px;
  }

  .pv-subject {
    font-weight: 600;
    font-size: 0.82em;
    color: var(--text-color);
    margin-bottom: 6px;
    word-break: break-word;
  }

  .pv-ok {
    color: var(--success-color);
  }

  .pv-bad {
    color: var(--error-color);
  }

  /* The header the receiver will be told to expect, beside the body it will get.
     Small and monospace: it is a fact about the request, not a control. */
  .pv-ctype {
    font-size: 0.72em;
    color: var(--text-muted);
    background: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    padding: 1px 5px;
  }

  .pv-size {
    font-size: 0.72em;
    color: var(--text-muted);
  }

  .pv-note {
    margin: 6px 0 0;
    font-size: 0.72em;
    color: var(--text-muted);
  }

  .pv-error {
    margin: 4px 0 0;
    font-size: 0.72em;
    color: var(--error-color);
    word-break: break-word;
  }

  .pv-json {
    color: var(--text-color);
  }

  .pv-text {
    margin: 0;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.75em;
    color: var(--text-muted);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 190px;
    overflow-y: auto;
    flex: 1 1 auto;
    min-height: 0;
  }
</style>
