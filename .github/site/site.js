export function fallbackCopy(text, doc = document) {
  const input = doc.createElement('textarea');
  input.value = text;
  input.setAttribute('readonly', '');
  input.style.position = 'fixed';
  input.style.opacity = '0';
  doc.body.appendChild(input);
  input.select();
  const copied = doc.execCommand('copy');
  input.remove();
  return copied;
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
        if (fallbackCopy(button.dataset.copy)) {
          showResult('COPIED');
          return;
        }

        const range = document.createRange();
        range.selectNodeContents(button.querySelector('code'));
        window.getSelection().removeAllRanges();
        window.getSelection().addRange(range);
        showResult('SELECTED');
      }
    });
  }
}
