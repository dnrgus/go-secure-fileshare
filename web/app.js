(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);
  const dropzone = $('dropzone');
  const input = $('file');
  const picked = $('picked');
  const submit = $('submit');
  const bar = $('progress');
  const errBox = $('error');
  const result = $('result');

  let file = null;
  let maxBytes = 0;

  // 서버 제한을 안내용으로 표시한다. (클라이언트 검사는 편의일 뿐, 실제 검증은 항상 서버가 한다)
  fetch('/api/limits')
    .then((r) => r.json())
    .then((l) => {
      maxBytes = l.max_upload_bytes;
      $('limits').textContent =
        `최대 ${Math.floor(l.max_upload_bytes / 1048576)}MB · ${l.ttl_hours}시간 보관 후 자동 삭제 · 허용 확장자: ${l.allowed.join(' ')}`;
    })
    .catch(() => {});

  // curl 예시에 현재 접속 주소를 채워 넣는다.
  const origin = location.origin;
  $('curl').textContent = [
    '# 업로드 (multipart) — 첫 줄이 공유 링크',
    `curl -F "file=@./report.pdf" ${origin}/upload`,
    '',
    '# 업로드 (PUT) — 주소 끝의 / 때문에 로컬 파일명이 자동으로 붙음',
    `curl -T ./report.pdf ${origin}/upload/`,
    '',
    '# JSON 으로 받기',
    `curl -H "Accept: application/json" -F "file=@./report.pdf" ${origin}/upload`,
    '',
    '# 다운로드 (URL 마지막 조각이 파일명)',
    `curl -O ${origin}/f/<id>/<파일명>`,
    '',
    '# 삭제 (업로드 때 받은 삭제 키 필요)',
    `curl -X DELETE -H "X-Delete-Key: <키>" ${origin}/f/<id>`,
  ].join('\n');

  function showError(msg) {
    errBox.textContent = msg; // textContent 만 사용 → XSS 방지
    errBox.hidden = false;
  }

  function pick(f) {
    errBox.hidden = true;
    result.hidden = true;
    file = f || null;
    if (!file) {
      picked.textContent = '선택된 파일 없음';
      submit.disabled = true;
      return;
    }
    picked.textContent = `${file.name} (${(file.size / 1024).toFixed(1)} KB)`;
    if (maxBytes && file.size > maxBytes) {
      showError('파일이 서버 최대 크기를 넘습니다.');
      submit.disabled = true;
      return;
    }
    submit.disabled = false;
  }

  input.addEventListener('change', () => pick(input.files[0]));
  dropzone.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      input.click();
    }
  });
  ['dragenter', 'dragover'].forEach((ev) =>
    dropzone.addEventListener(ev, (e) => {
      e.preventDefault();
      dropzone.classList.add('over');
    })
  );
  ['dragleave', 'drop'].forEach((ev) =>
    dropzone.addEventListener(ev, (e) => {
      e.preventDefault();
      dropzone.classList.remove('over');
    })
  );
  dropzone.addEventListener('drop', (e) => pick(e.dataTransfer.files[0]));

  submit.addEventListener('click', () => {
    if (!file) return;
    const fd = new FormData();
    fd.append('file', file);

    const xhr = new XMLHttpRequest(); // fetch 는 업로드 진행률을 못 줘서 XHR 사용
    xhr.open('POST', '/upload');
    xhr.setRequestHeader('Accept', 'application/json');
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) {
        bar.max = e.total;
        bar.value = e.loaded;
      }
    };
    xhr.onloadend = () => {
      bar.hidden = true;
      submit.disabled = false;
    };
    xhr.onload = () => {
      let data = {};
      try {
        data = JSON.parse(xhr.responseText);
      } catch (_) {}
      if (xhr.status === 201) {
        errBox.hidden = true;
        $('link').value = data.url;
        $('expires').textContent = new Date(data.expires_at).toLocaleString();
        $('sha').textContent = data.sha256;
        $('delkey').textContent = data.delete_key;
        result.hidden = false;
      } else {
        showError(data.error || `업로드 실패 (HTTP ${xhr.status})`);
      }
    };
    xhr.onerror = () => showError('네트워크 오류로 업로드하지 못했습니다.');

    errBox.hidden = true;
    result.hidden = true;
    bar.value = 0;
    bar.hidden = false;
    submit.disabled = true;
    xhr.send(fd);
  });

  $('copy').addEventListener('click', async () => {
    const link = $('link');
    try {
      await navigator.clipboard.writeText(link.value);
      $('copy').textContent = '복사됨';
    } catch (_) {
      link.select();
      document.execCommand('copy');
    }
    setTimeout(() => ($('copy').textContent = '복사'), 1500);
  });
})();
