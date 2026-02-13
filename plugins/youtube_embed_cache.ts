export interface TransformResult {
  html: string;
  videoIds: string[];
}

const YOUTUBE_IFRAME_REGEX = /<iframe[^>]*\bsrc=(['"])(?:(?:https?:)?\/\/)?(?:www\.)?youtube\.com\/embed\/([A-Za-z0-9_-]{6,})[^'"]*\1[^>]*>\s*<\/iframe>/gis;

export function transform(html: string): TransformResult {
  const videoIds = new Set<string>();

  const rewritten = html.replace(YOUTUBE_IFRAME_REGEX, (_match, _quote, videoId: string) => {
    videoIds.add(videoId);
    return `<iframe frameborder="0" allowfullscreen style="border: none;position: absolute;top: 0;left: 0;width: 100%;height: 100%;" src="//www.youtube.com/embed/${videoId}"></iframe>`;
  });

  return {
    html: rewritten,
    videoIds: Array.from(videoIds),
  };
}
