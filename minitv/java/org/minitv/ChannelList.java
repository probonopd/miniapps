package org.minitv;

import android.content.Context;
import android.graphics.Color;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.animation.AccelerateInterpolator;
import android.view.animation.DecelerateInterpolator;
import android.widget.BaseAdapter;
import android.widget.ListView;
import android.widget.TextView;
import java.util.List;

/** The list of all channels, opened with OK. The selected one also shows what is on, once that is known. */
final class ChannelList extends ListView {
	private List<String> names;
	private String[] details;
	private int cursor;
	private boolean closing;
	private static final int SLIDE_MS = 300;

	private final BaseAdapter adapter = new BaseAdapter() {
		@Override
		public int getCount() {
			return names == null ? 0 : names.size();
		}

		@Override
		public Object getItem(int position) {
			return names.get(position);
		}

		@Override
		public long getItemId(int position) {
			return position;
		}

		@Override
		public View getView(int position, View convert, ViewGroup parent) {
			TextView row = convert instanceof TextView ? (TextView) convert : new TextView(getContext());
			boolean selected = position == cursor;
			row.setTextColor(Color.WHITE);
			row.setTextSize(selected ? 26 : 24);
			row.setGravity(Gravity.CENTER_VERTICAL);
			row.setPadding(40, 14, 40, 14);
			row.setBackgroundColor(selected ? 0xCC2060C0 : Color.TRANSPARENT);
			String detail = selected ? details[position] : null;
			row.setText(detail == null ? names.get(position) : names.get(position) + "\n" + detail);
			return row;
		}
	};

	ChannelList(Context context) {
		super(context);
		setBackgroundColor(0xB0000000);
		setDivider(null);
		setFocusable(false);
		setVerticalScrollBarEnabled(false);
		setVisibility(GONE);
		setAdapter(adapter);
	}

	void open(List<String> channels, int current) {
		names = channels;
		details = new String[channels.size()];
		cursor = current;
		// Slides in from the left while fading in.
		animate().cancel();
		closing = false;
		boolean wasHidden = getVisibility() != VISIBLE;
		setVisibility(VISIBLE);
		refresh();
		if (wasHidden) {
			setAlpha(0f);
			setTranslationX(-slideWidth());
			animate().alpha(1f).translationX(0f).setDuration(SLIDE_MS).setInterpolator(new DecelerateInterpolator()).start();
		} else {
			setAlpha(1f);
			setTranslationX(0f);
		}
	}

	/** Slides out to the left while fading out. */
	void close() {
		if (getVisibility() != VISIBLE || closing) return;
		closing = true;
		animate().cancel();
		animate().alpha(0f).translationX(-slideWidth()).setDuration(SLIDE_MS).setInterpolator(new AccelerateInterpolator())
				.withEndAction(() -> {
					setVisibility(GONE);
					closing = false;
					setAlpha(1f);
					setTranslationX(0f);
				}).start();
	}

	private float slideWidth() {
		ViewGroup.LayoutParams params = getLayoutParams();
		return params != null && params.width > 0 ? params.width : getWidth();
	}

	/** Open, and not on its way out: the keys belong to the list. */
	boolean isOpen() {
		return getVisibility() == VISIBLE && !closing;
	}

	int cursor() {
		return cursor;
	}

	void move(int step) {
		cursor = (cursor + step + names.size()) % names.size();
		refresh();
	}

	void setDetail(int index, String detail) {
		if (details == null || index >= details.length) return;
		details[index] = detail;
		refresh();
	}

	private void refresh() {
		adapter.notifyDataSetChanged();
		// Keeps the selected row a third from the top, so there is always context above and below.
		setSelectionFromTop(cursor, Math.max(getHeight() / 3, 0));
	}
}
