import { defineStore } from "pinia";
import { detectLocale, setLocale } from "@/i18n";
import { cloneDeep } from "lodash-es";

export const useAuthStore = defineStore("auth", {
  // convert to a function
  state: (): {
    user: IUser | null;
    jwt: string;
    // cancels the inactivity logout of the current token
    logoutTimer: (() => void) | null;
  } => ({
    user: null,
    jwt: "",
    logoutTimer: null,
  }),
  getters: {
    // user and jwt getter removed, no longer needed
    isLoggedIn: (state) => state.user !== null,
  },
  actions: {
    // no context as first argument, use `this` instead
    setUser(user: IUser) {
      if (user === null) {
        this.user = null;
        return;
      }

      setLocale(user.locale || detectLocale());
      this.user = user;
    },
    updateUser(user: Partial<IUser>) {
      // an answer for a session that has ended since (logout, another
      // user on the tab) is not about this one
      if (
        this.user === null ||
        (user.id !== undefined && user.id !== this.user.id)
      ) {
        return;
      }
      if (user.locale) {
        setLocale(user.locale);
      }

      this.user = { ...this.user, ...cloneDeep(user) } as IUser;
    },
    // easily reset state using `$reset`
    clearUser() {
      this.$reset();
    },
    setLogoutTimer(logoutTimer: (() => void) | null) {
      this.logoutTimer?.();
      this.logoutTimer = logoutTimer;
    },
  },
});
