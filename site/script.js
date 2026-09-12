(function () {
  var video = document.getElementById('video');
  var playPauseBtn = document.getElementById('playPauseBtn');
  var loopBtn = document.getElementById('loopBtn');

  function showPaused() {
    video.style.display = 'none';
    playPauseBtn.innerHTML = '&#9654;';
    playPauseBtn.setAttribute('aria-label', 'Play');
  }
  function showPlaying() {
    video.style.display = '';
    playPauseBtn.innerHTML = '&#10074;&#10074;';
    playPauseBtn.setAttribute('aria-label', 'Pause');
  }

  playPauseBtn.addEventListener('click', function () {
    if (video.style.display === 'none') {
      video.currentTime = 0;
      video.play();
      showPlaying();
    } else {
      video.pause();
      showPaused();
    }
  });

  loopBtn.addEventListener('click', function () {
    video.loop = !video.loop;
    loopBtn.classList.toggle('active', video.loop);
    loopBtn.setAttribute('aria-label', video.loop ? 'Turn off loop' : 'Turn on loop');
  });

  video.addEventListener('ended', function () {
    if (!video.loop) {
      showPaused();
    }
  });
})();
