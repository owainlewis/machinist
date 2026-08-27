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

if (typeof document !== 'undefined') {
  const copyButtons = document.querySelectorAll('[data-copy]');

  for (const button of copyButtons) {
    button.addEventListener('click', async () => {
      const label = button.querySelector('span');
      const showResult = (result) => {
        label.textContent = result;
        window.setTimeout(() => { label.textContent = 'COPY'; }, 1600);
      };

      try {
        await navigator.clipboard.writeText(button.dataset.copy);
        showResult('COPIED');
      } catch {
        let copied = false;
        try {
          copied = fallbackCopy(button.dataset.copy);
        } catch {
          copied = false;
        }
        if (copied) {
          showResult('COPIED');
          return;
        }

        selectCommand(button);
        showResult('SELECTED');
      }
    });
  }
}
