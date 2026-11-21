export function fallbackCopy(text, doc = document) {
  const input = doc.createElement('textarea');
  input.value = text;
  input.setAttribute('readonly', '');
  input.style.position = 'fixed';
  input.style.opacity = '0';
  doc.body.appendChild(input);
  try {
    input.select();
    return doc.execCommand('copy');
  } finally {
    input.remove();
  }
}

export function selectCommand(button, doc = document, selection = window.getSelection()) {
  const code = button.querySelector('code');
  code.textContent = button.dataset.copy;
  const range = doc.createRange();
  range.selectNodeContents(code);
  selection.removeAllRanges();
  selection.addRange(range);
}

// Turning sound on restarts the explainer, so the viewer hears it from the start.
export function toggleSound(video, button) {
  const soundOn = video.muted;
  video.muted = !soundOn;
  if (soundOn) {
    video.currentTime = 0;
    video.play();
  }
  button.textContent = soundOn ? 'Sound off' : 'Sound on';
  button.setAttribute('aria-pressed', String(soundOn));
  return soundOn;
}

if (typeof document !== 'undefined') {
  const video = document.getElementById('explainer');
  const soundButton = document.querySelector('[data-sound]');
  if (video && soundButton) {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      video.removeAttribute('autoplay');
      video.pause();
      video.controls = true;
    }
    soundButton.addEventListener('click', () => toggleSound(video, soundButton));
  }

  const copyButtons = document.querySelectorAll('[data-copy]');

  for (const button of copyButtons) {
    button.addEventListener('click', async () => {
      const label = button.querySelector('span');
      const showResult = (result) => {
        label.textContent = result;
        window.setTimeout(() => { label.textContent = 'Copy'; }, 1600);
      };

      try {
        await navigator.clipboard.writeText(button.dataset.copy);
        showResult('Copied');
      } catch {
        let copied = false;
        try {
          copied = fallbackCopy(button.dataset.copy);
        } catch {
          copied = false;
        }
        if (copied) {
          showResult('Copied');
          return;
        }

        selectCommand(button);
        showResult('Selected');
      }
    });
  }
}
